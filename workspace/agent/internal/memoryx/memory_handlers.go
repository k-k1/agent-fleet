package memoryx

// エージェントメモリの版管理（docs/log/39 / ADR 0022）— REST
// （P1: roots / snapshots / diff、P2: tree / restore / settings、P3: export / import）。
//
// ⚠️ ここに足したパスは control-plane/routes.go にも同じものを登録する（CP は明示許可
// リスト方式で、片側漏れ = Console から 404）。

import (
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// memoryRootView は roots API が返す 1 ルート分。
type memoryRootView struct {
	Kind     string             `json:"kind"`
	Label    string             `json:"label"`
	Scopes   bool               `json:"scopes"` // プロジェクト粒度の部分ロールバックが可能か
	Files    int                `json:"files"`
	Bytes    int64              `json:"bytes"`
	Modified string             `json:"modified,omitempty"` // 最新 mtime（RFC3339）
	Busy     bool               `json:"busy"`               // この kind に実行中セッションがある
	Projects []memoryProjectRef `json:"projects"`           // claude のみ（scopes=false は空）
	// Toggleable / Enabled は「エージェント側がメモリを書くこと」の ON/OFF（docs/log/39 P4）。
	// 有効なルートにも載せる — codex が一度ワークスペースを作ると inactive から消えるので、
	// ここに無いと有効化した後に切り戻す導線が UI から失われる。
	Toggleable bool `json:"toggleable,omitempty"`
	Enabled    bool `json:"enabled,omitempty"`
}

// memoryWriteErr は memoryUserErr（入力起因の失敗）を安定コードへ、それ以外を
// fallback（500）へ写す。500 側はログにも残す — 応答の message は Console の i18n が
// 汎用文言へ畳んでしまうので、現地で起きた失敗を後から追える場所がここしかない。
func memoryWriteErr(w http.ResponseWriter, err error, fallback string) {
	var ue *memoryUserErr
	if errors.As(err, &ue) {
		httpx.WriteErr(w, ue.Status, ue.Code, ue.Msg)
		return
	}
	log.Printf("memory: %s: %v", fallback, err)
	httpx.WriteErr(w, http.StatusInternalServerError, fallback, err.Error())
}

// handleMemoryRoots はこの環境で有効なメモリルートと、その中身の概況を返す。
// codex は ~/.codex/memories が存在するときだけ現れる（memories 機能は既定 OFF）。
func handleMemoryRoots(w http.ResponseWriter, r *http.Request) {
	roots := memoryRoots()
	busy := memoryBusyKinds()
	views := make([]memoryRootView, 0, len(roots))
	for _, root := range roots {
		v := memoryRootView{
			Kind: root.Kind, Label: root.Label, Scopes: root.Scopes,
			Busy: busy[root.Kind], Projects: []memoryProjectRef{},
		}
		if root.Kind == "codex" {
			// 無効にしても既存の md は残り、版管理の対象からは外れない
			// （codex が更新しなくなるだけ）。履歴が欠けないのが正しい。
			v.Toggleable, v.Enabled = true, codex.MemoriesEnabled()
		}
		var newest time.Time
		seen := map[string]bool{}
		for _, f := range memoryCollect(root) {
			v.Files++
			v.Bytes += f.Size
			if t := time.Unix(f.MTime, 0); t.After(newest) {
				newest = t
			}
			if !root.Scopes {
				continue
			}
			if slug, ok := memoryScopeSlug(root.RepoPrefix + "/" + f.Rel); ok && !seen[slug] {
				seen[slug] = true
				v.Projects = append(v.Projects, memoryProjectRef{Slug: slug, Display: memorySlugDisplay(slug)})
			}
		}
		if !newest.IsZero() {
			v.Modified = newest.Format(time.RFC3339)
		}
		views = append(views, v)
	}
	out := map[string]any{
		"roots": views,
		// 宣言はあるが今は有効でないルート（codex memories が未有効 等）を理由付きで
		// 返す。黙って落とすと Console が「なぜ出てこないか」も「どう有効化するか」も
		// 示せない（docs/log/39 P4）。
		"inactive": memoryInactiveRoots(),
		"auto":     memoryAutoEnabled(),
		// locked = 運用側が AF_MEMORY_SNAPSHOT で止めている（UI トグルでは戻せない）。
		"autoLocked": memoryAutoLocked(),
	}
	if head := memoryHeadTime(); !head.IsZero() {
		out["lastSnapshot"] = head.Format(time.RFC3339)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleMemorySettings は自動 snapshot の ON/OFF トグル（docs/log/39 決着 #1）。
// 環境変数による強制 OFF は上書きできない。
func handleMemorySettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Auto *bool `json:"auto"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if body.Auto == nil {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRequest, "auto is required")
		return
	}
	if err := memorySetAuto(*body.Auto); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemorySnapshotFailed, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"auto": memoryAutoEnabled(), "autoLocked": memoryAutoLocked()})
}

// handleMemorySnapshots は snapshot 履歴を新しい順に返す（?limit=&before=）。
func handleMemorySnapshots(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRequest, "limit must be a positive integer")
			return
		}
		limit = n
	}
	list, err := memoryListSnapshots(limit, r.URL.Query().Get("before"))
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"snapshots": list})
}

// handleMemorySnapshotCreate は手動 snapshot（docs/log/39 ②）。変更が無ければ committed=false
// を返すだけで、空コミットは積まない。
func handleMemorySnapshotCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Trigger string `json:"trigger"`
	}
	if r.ContentLength > 0 && !httpx.DecodeJSON(w, r, &body) {
		return
	}
	// 契機は API から任意の文字列を刻ませない（履歴の意味が壊れる）。手動経路は manual 固定。
	if body.Trigger != "" && body.Trigger != memoryTriggerManual {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRequest, "trigger must be \"manual\"")
		return
	}
	res, err := memorySnapshot(memoryTriggerManual, time.Now())
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemorySnapshotFailed, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// handleMemoryDiff は 2 時点間の unified diff を返す（?from=&to=&at=&path=）。
// from 省略で「to が入れた変更」、path 省略で全体。at は「その時刻以前の直近 snapshot」。
func handleMemoryDiff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !memoryHasCommits() {
		httpx.WriteErr(w, http.StatusNotFound, errCodeMemoryNoSnapshots, "no snapshots yet")
		return
	}
	to, err := memoryResolveRev(q.Get("to"), q.Get("at"))
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRev, err.Error())
		return
	}
	from := ""
	if v := q.Get("from"); v != "" {
		if from, err = memoryResolveRev(v, ""); err != nil {
			httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRev, err.Error())
			return
		}
	}
	path := q.Get("path")
	if !memoryPathSafe(path) {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadPath, "path must be inside a declared memory root")
		return
	}
	diff, err := memoryDiff(from, to, path)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemoryDiffFailed, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "path": path, "diff": diff})
}

// handleMemoryTree は「その時点に何が入っていたか」を返す（?rev=|at=）。restore の
// スコープ選択はこれを見る — 現在の roots を選択肢にすると、既に消えたプロジェクトを
// 選べず「誤って消したメモリを戻す」という本命が成立しないため（memory_restore.go）。
func handleMemoryTree(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rev := q.Get("rev")
	if rev == "" {
		rev = q.Get("to") // diff API と同じ綴りでも受ける
	}
	sha, kinds, projects, err := memoryTreeAt(rev, q.Get("at"))
	if err != nil {
		memoryWriteErr(w, err, errCodeMemoryDiffFailed)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"rev": sha, "kinds": kinds, "projects": projects})
}

// handleMemoryRestore は指定時点への巻き戻し（docs/log/39 ④）。履歴は書き換えず、
// pre-restore snapshot → live へ適用 → restore commit の 3 つを積む。
func handleMemoryRestore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Rev   string             `json:"rev"`
		At    string             `json:"at"`
		Scope memoryRestoreScope `json:"scope"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	res, err := memoryRestore(body.Scope, body.Rev, body.At, time.Now())
	if err != nil {
		memoryWriteErr(w, err, errCodeMemoryRestoreFailed)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// handleMemoryImport は bundle / tar.gz を受領し、refs/imports/<id> へ独立系譜として
// 取り込んで preview を返す（docs/log/39 ⑤）。この時点では live に一切触れない — 何を
// 適用するかは利用者が preview を見て決める。
func handleMemoryImport(w http.ResponseWriter, r *http.Request) {
	if err := memoryEnsureRepo(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemoryImportFailed, err.Error())
		return
	}
	if err := os.MkdirAll(memoryWorkDir(), 0o700); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemoryImportFailed, err.Error())
		return
	}
	// 受領はストリームで行い、メモリに丸ごと載せない（★3 サイズ上限もここで効かせる）。
	mr, err := r.MultipartReader()
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRequest, "expected multipart/form-data")
		return
	}
	max := memoryImportMaxBytes()
	tmp, name := "", ""
	for {
		part, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRequest, perr.Error())
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			continue
		}
		f, cerr := os.CreateTemp(memoryWorkDir(), "upload-*")
		if cerr != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemoryImportFailed, cerr.Error())
			return
		}
		n, werr := io.Copy(f, io.LimitReader(part, max+1))
		_ = f.Close()
		if werr != nil || n > max {
			_ = os.Remove(f.Name())
			if n > max {
				httpx.WriteErr(w, http.StatusRequestEntityTooLarge, errCodeMemoryTooLarge, "file exceeds the import size limit")
			} else {
				httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemoryImportFailed, "upload failed")
			}
			return
		}
		tmp, name = f.Name(), filepath.Base(part.FileName())
		break
	}
	if tmp == "" {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRequest, "no file part in the request")
		return
	}
	defer os.Remove(tmp)

	pv, err := memoryImportPrepare(tmp, name, time.Now())
	if err != nil {
		memoryWriteErr(w, err, errCodeMemoryImportFailed)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, pv)
}

// handleMemoryImportApply は取り込んだ系譜から、選んだプロジェクト / kind だけを live へ
// 適用する（置き換え = 新しい commit。3-way merge はしない — ADR 0022 決定 5）。
// mode="migrate" は移設 = 内容に加えて**履歴も**取り込んだ系譜へ入れ替える（範囲は全体固定）。
// 経路を増やさず 1 キーで分けるのは、REST を足すと CP 側の許可リスト登録漏れという既知の
// 罠（memory_handlers.go 冒頭の ⚠️）を踏むため。
func handleMemoryImportApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ImportID string             `json:"importId"`
		Scope    memoryRestoreScope `json:"scope"`
		Mode     string             `json:"mode"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if body.Mode != "" && body.Mode != memoryImportModeReplace && body.Mode != memoryImportModeMigrate {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRequest,
			"mode must be \""+memoryImportModeReplace+"\" or \""+memoryImportModeMigrate+"\"")
		return
	}
	opts := memoryApplyOpts{Adopt: body.Mode == memoryImportModeMigrate}
	res, err := memoryImportApply(body.ImportID, body.Scope, time.Now(), opts)
	if err != nil {
		memoryWriteErr(w, err, errCodeMemoryImportFailed)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}
