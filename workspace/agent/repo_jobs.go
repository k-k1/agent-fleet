package main

// リポジトリ取り込みジョブ（docs/78）。`git clone` / `svn checkout` を **HTTP リクエストの
// 寿命から切り離した名前付きジョブ**として走らせ、進捗と結末を別 API で観測できるようにする。
//
// なぜ要るか（実測した事故そのもの）: 大きな取り込みは分〜時間かかるが、ALB の idle timeout は
// 60 秒（deploy/aws/ecs/cfn/30-ingress.yaml）。同期 POST では必ずここで応答が切れ、Console は
// 「フォルダができていれば成功」と読み替えて **走っている最中のチェックアウトを完了と報告**して
// いた。利用者から見ると中途半端な作業コピーが「取り込み済み」として並び、そこへ `svn update` を
// 打つと走行中の checkout と sqlite ロックを奪い合って E155037／E200033 になる。さらに 30 分の
// 上限を越えると失敗パスがフォルダごと削除し、**数十分ダウンロードした作業コピーが黙って消えた**。
//
// 設計の要点:
//   - POST は検証だけ同期で行い、202 とジョブを返す。ネットワーク処理は background で走る。
//   - 取り込み中のフォルダは `GET /repos` に出さない。作業コピーとして使えない物を一覧に出すと、
//     そこで起動でき、更新でき、`svn status` を掛けてしまう（走行中の checkout と競合する）。
//   - 進捗は行を数える。checkout/clone の出力を全部ためると巨大リポジトリでメモリを食うので、
//     カウンタ＋最終行＋末尾リングだけ保持する（エラー本文には末尾リングを使う）。
//   - 失敗しても **再開できる作業コピーは消さない**。svn は cleanup + update で続きから取れる。
//   - Agent が落ちて（ECS のタスク入れ替え・idle-stop）ジョブごと消えた場合に備え、marker を
//     ディスクに置く。起動時に生き残った marker ＝ 中断。これを interrupted として出さないと、
//     半端な作業コピーが「普通のリポジトリ」として一覧に戻ってしまう（元の事故と同じ状態）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// ジョブの状態。running 以外はすべて終端で、利用者が dismiss するまで一覧に残る
// （結末を見る前に消えると、また「黙って失敗した」になる）。
const (
	repoJobRunning     = "running"
	repoJobDone        = "done"
	repoJobFailed      = "failed"
	repoJobCanceled    = "canceled"
	repoJobInterrupted = "interrupted" // Agent 再起動でジョブごと消えた（marker の生き残り）
)

// repoJobDoneTTL は成功したジョブを一覧に残す時間。成功は Console がトーストで伝えるので
// 短くてよい。失敗・中断は TTL を持たない（利用者が dismiss するまで残す）。
const repoJobDoneTTL = 10 * time.Minute

// repoJobTimeout はジョブ全体の上限。30 分で殺して作業コピーを消していたのが元の事故なので、
// 「人が待てる上限」ではなく「明らかに壊れている」だけを切る値にしてある。止めたいときは
// cancel があり、走っていることは一覧で見える。
const repoJobTimeout = 6 * time.Hour

// repoJobProgressMax は進捗行の保持長。svn のパスは長くなりうるので切り詰める。
const repoJobProgressMax = 240

// repoJobTailMax はエラー本文に使う末尾バッファ。svn/git の最後の数十行あれば原因は分かる。
const repoJobTailMax = 8 << 10

// RepoJob は 1 件の取り込み。JSON はそのまま Console の行になる。
type RepoJob struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`  // "git" | "svn"
	Name  string `json:"name"`  // ~/repos 直下のフォルダ名
	Path  string `json:"path"`  //
	URL   string `json:"url"`   // 表示用。資格情報は載せない
	State string `json:"state"` //

	// Progress は最後に見えた出力行、Items は取得したファイル数（svn の "A path" /
	// git の checkout 行）。どちらも「進んでいる」ことを見せるためだけの近似で、総数は
	// 分からない（svn も git も事前に総数を教えてくれない）。
	Progress string `json:"progress,omitempty"`
	Items    int    `json:"items,omitempty"`

	Error     string `json:"error,omitempty"`
	Kept      bool   `json:"kept,omitempty"` // 失敗したが作業コピーを残した（再開できる）
	StartedAt string `json:"startedAt"`
	EndedAt   string `json:"endedAt,omitempty"`
}

// repoJobEntry は登録簿の 1 件。RepoJob（外向き）に cancel と可変進捗を足したもの。
type repoJobEntry struct {
	job    RepoJob
	cancel func()
	sink   *repoJobSink
}

var repoJobs = struct {
	mu   sync.Mutex
	m    map[string]*repoJobEntry // id -> entry
	seq  int
	swpt bool // 起動時の marker 走査を済ませたか
}{m: map[string]*repoJobEntry{}}

// repoJobMarkerDir は中断検出用の marker 置き場。~/repos の下に置くのは意図的で、
// 「作業コピーと同じ寿命」だから（コンテナ作り直しで ~/repos ごと消え、marker も消える）。
func repoJobMarkerDir() string { return filepath.Join(reposRoot(), ".af-repo-jobs") }

func repoJobMarkerPath(name string) string {
	return filepath.Join(repoJobMarkerDir(), name+".json")
}

// repoJobMarker はディスクに残す最小限。中断として一覧に戻すのに要るものだけ。
type repoJobMarker struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	StartedAt string `json:"startedAt"`
}

func writeRepoJobMarker(j RepoJob) {
	if err := os.MkdirAll(repoJobMarkerDir(), 0o755); err != nil {
		return
	}
	b, err := json.Marshal(repoJobMarker{ID: j.ID, Kind: j.Kind, Name: j.Name, URL: j.URL, StartedAt: j.StartedAt})
	if err != nil {
		return
	}
	_ = os.WriteFile(repoJobMarkerPath(j.Name), b, 0o644)
}

func removeRepoJobMarker(name string) { _ = os.Remove(repoJobMarkerPath(name)) }

// sweepRepoJobMarkers は起動時に一度だけ走り、生き残った marker を interrupted として復元する。
// 「Agent が落ちた＝取り込みも死んだ」を利用者に見せるための唯一の手段（プロセスは道連れなので、
// 何もしなければ半端な作業コピーだけが黙って残る）。
func sweepRepoJobMarkers() {
	repoJobs.mu.Lock()
	if repoJobs.swpt {
		repoJobs.mu.Unlock()
		return
	}
	repoJobs.swpt = true
	repoJobs.mu.Unlock()

	entries, err := os.ReadDir(repoJobMarkerDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(repoJobMarkerDir(), e.Name()))
		if err != nil {
			continue
		}
		var m repoJobMarker
		if json.Unmarshal(b, &m) != nil || m.Name == "" {
			continue
		}
		dir, ok := resolveRepoDir(m.Name)
		if !ok {
			continue
		}
		kept := isGitRepo(dir) || isSvnRepo(dir)
		if _, err := os.Stat(dir); err != nil {
			// フォルダごと消えている（利用者が消した／取り込みが始まる前に落ちた）。
			// 報告する相手のいない中断なので marker だけ片付ける。
			removeRepoJobMarker(m.Name)
			continue
		}
		repoJobs.mu.Lock()
		repoJobs.m[m.ID] = &repoJobEntry{job: RepoJob{
			ID: m.ID, Kind: m.Kind, Name: m.Name, Path: dir, URL: m.URL,
			State: repoJobInterrupted, Kept: kept, StartedAt: m.StartedAt,
			EndedAt: time.Now().Format(time.RFC3339),
			Error:   "the workspace stopped (or the agent restarted) while this import was running",
		}}
		repoJobs.mu.Unlock()
		removeRepoJobMarker(m.Name)
	}
}

// repoJobSink は取り込みコマンドの出力を「行数・最終行・末尾」に畳む io.Writer。
// CombinedOutput のように全部ためない: 巨大リポジトリの checkout は数十万行になる。
type repoJobSink struct {
	mu       sync.Mutex
	items    int
	progress string
	tail     []byte
	partial  []byte
}

func (s *repoJobSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tail = append(s.tail, p...)
	if len(s.tail) > repoJobTailMax {
		s.tail = append([]byte(nil), s.tail[len(s.tail)-repoJobTailMax:]...)
	}
	// git は進捗を \r で上書きするので改行と同じ区切りとして扱う。
	s.partial = append(s.partial, p...)
	for {
		i := bytesIndexAny(s.partial, "\n\r")
		if i < 0 {
			break
		}
		line := strings.TrimSpace(string(s.partial[:i]))
		s.partial = s.partial[i+1:]
		if line == "" {
			continue
		}
		s.items++
		if len(line) > repoJobProgressMax {
			line = line[:repoJobProgressMax]
		}
		s.progress = line
	}
	if len(s.partial) > repoJobProgressMax*4 {
		s.partial = s.partial[len(s.partial)-repoJobProgressMax:]
	}
	return len(p), nil
}

func (s *repoJobSink) snapshot() (items int, progress string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.items, s.progress
}

func (s *repoJobSink) tailString() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(string(s.tail))
}

// bytesIndexAny は改行系の最初の位置。strings.IndexAny の []byte 版（1 バイト文字だけ探す）。
func bytesIndexAny(b []byte, chars string) int {
	for i, c := range b {
		if strings.IndexByte(chars, c) >= 0 {
			return i
		}
	}
	return -1
}

// repoJobActive は name の取り込みが今走っているか。一覧（GET /repos）と削除の門。
func repoJobActive(name string) bool {
	repoJobs.mu.Lock()
	defer repoJobs.mu.Unlock()
	for _, e := range repoJobs.m {
		if e.job.Name == name && e.job.State == repoJobRunning {
			return true
		}
	}
	return false
}

// repoJobsRunning は走行中の件数。GET /sessions に載せて CP の idle-stop に
// 「この Workspace は仕事中」と伝える（取り込み中に止められると作業コピーが壊れる）。
func repoJobsRunning() int {
	repoJobs.mu.Lock()
	defer repoJobs.mu.Unlock()
	n := 0
	for _, e := range repoJobs.m {
		if e.job.State == repoJobRunning {
			n++
		}
	}
	return n
}

// listRepoJobs は表示用の一覧。走行中は進捗を反映し、期限切れの成功は落とす。
func listRepoJobs() []RepoJob {
	now := time.Now()
	repoJobs.mu.Lock()
	defer repoJobs.mu.Unlock()
	out := []RepoJob{}
	for id, e := range repoJobs.m {
		if e.job.State == repoJobDone {
			if t, err := time.Parse(time.RFC3339, e.job.EndedAt); err == nil && now.Sub(t) > repoJobDoneTTL {
				delete(repoJobs.m, id)
				continue
			}
		}
		j := e.job
		if e.sink != nil {
			j.Items, j.Progress = e.sink.snapshot()
		}
		out = append(out, j)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].StartedAt > out[k].StartedAt })
	return out
}

// startRepoJob は検証済みの取り込みを background で開始する。run はネットワーク処理本体で、
// 渡された ctx を exec に繋ぎ、出力を sink に流すこと。戻り値はジョブの初期スナップショット
// （202 の本文）。ctx は repoJobTimeout を上限に持ち、cancel は DELETE から引ける。
func startRepoJob(kind, name, dir, url string, run func(ctx context.Context, sink *repoJobSink) error) RepoJob {
	ctx, cancel := context.WithTimeout(context.Background(), repoJobTimeout)
	repoJobs.mu.Lock()
	repoJobs.seq++
	id := fmt.Sprintf("rj%d-%d", time.Now().UnixNano(), repoJobs.seq)
	sink := &repoJobSink{}
	e := &repoJobEntry{
		job: RepoJob{
			ID: id, Kind: kind, Name: name, Path: dir, URL: url,
			State: repoJobRunning, StartedAt: time.Now().Format(time.RFC3339),
		},
		cancel: cancel,
		sink:   sink,
	}
	repoJobs.m[id] = e
	job := e.job
	repoJobs.mu.Unlock()

	writeRepoJobMarker(job)
	go func() {
		defer cancel()
		finishRepoJob(id, run(ctx, sink))
	}()
	return job
}

// finishRepoJob は終端へ遷移させ、marker を落とす。失敗時に作業コピーを残したかは
// run 側が判断済み（ここでは現状を見て Kept を立てるだけ）。
func finishRepoJob(id string, err error) {
	repoJobs.mu.Lock()
	e, ok := repoJobs.m[id]
	if !ok {
		repoJobs.mu.Unlock()
		return
	}
	e.job.EndedAt = time.Now().Format(time.RFC3339)
	if e.sink != nil {
		e.job.Items, e.job.Progress = e.sink.snapshot()
	}
	name, dir := e.job.Name, e.job.Path
	canceled := e.job.State == repoJobCanceled
	switch {
	case err == nil:
		e.job.State = repoJobDone
		e.job.Error = ""
	case canceled:
		e.job.Error = err.Error()
	default:
		e.job.State = repoJobFailed
		e.job.Error = err.Error()
	}
	if e.job.State != repoJobDone {
		e.job.Kept = isGitRepo(dir) || isSvnRepo(dir)
	}
	repoJobs.mu.Unlock()
	removeRepoJobMarker(name)
}

// markRepoJobCanceled は cancel 要求を記録して走行中のプロセスを殺す。実際の終端遷移は
// run が返ってきたときの finishRepoJob（そこで「消すか残すか」も決まる）。
func markRepoJobCanceled(id string) bool {
	repoJobs.mu.Lock()
	e, ok := repoJobs.m[id]
	if !ok || e.job.State != repoJobRunning {
		repoJobs.mu.Unlock()
		return false
	}
	e.job.State = repoJobCanceled
	cancel := e.cancel
	repoJobs.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return true
}

// dismissRepoJob は終端したジョブを一覧から消す（記録を読んだ、という利用者の意思表示）。
func dismissRepoJob(id string) bool {
	repoJobs.mu.Lock()
	defer repoJobs.mu.Unlock()
	e, ok := repoJobs.m[id]
	if !ok || e.job.State == repoJobRunning {
		return false
	}
	delete(repoJobs.m, id)
	return true
}

// handleListRepoJobs (GET /repos/jobs) — 取り込みの進行と結末。Console はこれを見て
// 「取り込み中」の行を描く。ブラウザを閉じても、別のタブでも、同じものが見える。
func handleListRepoJobs(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"jobs": listRepoJobs()})
}

// handleDeleteRepoJob (DELETE /repos/jobs/{id}) — 走行中なら中止、終端済みなら一覧から消す。
func handleDeleteRepoJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if markRepoJobCanceled(id) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"canceled": id})
		return
	}
	if dismissRepoJob(id) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"dismissed": id})
		return
	}
	httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such repo job: "+id)
}
