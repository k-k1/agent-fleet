package gitx

// deps.go — gitx が呼び出し側（package main）へ伸ばしている手を 1 枚に集めたもの。
//
// git 家系は「リポジトリを持つ」ことそのものなので、外向きの依存は main の各家系へ
// 散る（ロック台帳・セッションの生存・取り込みジョブ・SVN・資格情報ヘルパ・
// エラーコード表）。ここはその断面を隠すのではなく、**1 箇所に集めて数えられるように
// する**ための口である（internal/mcpx/deps.go と同じ形）:
//
//   - gitx は main を import しない（できない。逆向きの依存が既にある）
//   - なので「main の関数を呼ぶ」は関数値として受け取る形にする
//   - **配線は起動時に 1 回**（main の git_wiring.go の init）。Configure が欠けを
//     検査して落とす —— 配線漏れを既定値で黙って埋めると、たとえば削除の
//     ロック判定が「常に未ロック」になって**ロックしたはずのものが消える**。
//     静かに動くより落ちる方を選ぶ。
//
// gitx 単体のテストは main を持たないので、TestMain ではなく init が偽物を配線する
// （deps_test.go 参照）。

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"sort"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// RepoJobSink は main の `*repoJobSink` のうち **gitx が実際に使う面だけ**を書いた
// consumer-defined interface（README §1 ③）。
//
// ⚠️ main の型をそのまま受け取ることはできない（型を 1 つでも入れた瞬間に断面が
// 閉じなくなる）。かといって `io.Writer` だけでは足りない —— clone は失敗したときに
// **それまでの出力の末尾**をエラー本文として返しており、そこが落ちると
// 「clone に失敗しました」だけが Console に出て理由が消える。
//
// 🔥 main 側のメソッドは `tailString()` で**未公開**である。未公開メソッドは宣言した
// パッケージに紐づくので、gitx がこの綴りで interface を書いても main の型は満たさない。
// だから名前を変えて `Tail()` とし、**main 側（git_wiring.go）で薄い adapter を被せる**。
// repo_jobs.go は AG-GIT の所有外なので、そちらへメソッドを足す選択肢は無い。
type RepoJobSink interface {
	io.Writer
	// Tail は main の (*repoJobSink).tailString —— それまでに書き込まれた出力の末尾。
	Tail() string
}

// Deps は「gitx から見た外の世界」。**型は main のものを 1 つも含まない**
// （含んだ瞬間に切断面が閉じなくなる）ので、増えても import は増えない。
type Deps struct {
	// --- ロック台帳（locks.go）---
	//
	// 削除・チェックアウト・worktree の刈り取りが「消してよいか」を決める唯一の根拠。
	// 未配線を零値で埋めると **repoLocked が常に false ＝ ロックが無いのと同じ**に
	// なるので、Configure が落とす側に倒してある。
	AbsPath        func(p string) string
	RepoLocked     func(dir string) bool
	LockedRepoDirs func() map[string]bool

	// --- セッションの生存（session_tmux.go / session_handlers.go）---
	//
	// 「このワークツリーはまだ誰かが使っているか」。これも未配線が
	// 「誰も使っていない」に化けると、**動いているセッションのワークツリーを消す**。
	LiveSessionsInDir   func(dir string) []string
	LockedSessionsInDir func(metas []session.Meta, dir string) []string
	WorktreeHasSessions func(dir string) bool
	ManagedAlive        func(m session.Meta) bool

	// --- 使用量の締め（usage_fold.go）---
	FinalizeSessionUsage func(m session.Meta)

	// --- 取り込みジョブ（repo_jobs.go）---
	//
	// clone は要求より長く生きるので背景ジョブで走る（docs/log/78）。戻り値の
	// `RepoJob` は main の型なので `any` で受ける —— gitx は JSON にして返すだけで、
	// 中身を読まない。
	RepoJobActive func(name string) bool
	StartRepoJob  func(kind, name, dir, url string, run func(ctx context.Context, sink RepoJobSink) error) any

	// --- SVN（svn.go）---
	//
	// ~/repos は git と svn が混在する。一覧は両方を並べるので、git 側から svn を
	// 引く必要がある。**SvnRepoEntry は gitx.Repo を返す**（Repo は移送で gitx へ
	// 来たので、これは main の型ではない）。
	IsSvnRepo    func(dir string) bool
	SvnRepoEntry func(name, dir string) Repo

	// --- 資格情報ヘルパ（cred_helper.go）---
	EnsureCredHelper func() error
	InternalGitHost  func() string

	// --- 接続（connections.go）---
	//
	// GitHosts は「対応プロバイダのホスト → 既定の git ユーザ名」。**値を gitx 側で
	// 持ち直さない**（層ごとに違う表を持つと、片方だけ増えた日に無言で壊れる）。
	FirstNonEmpty   func(vals ...string) string
	GitConfigGlobal func(key, val string) error
	GitHosts        map[string]string

	// --- /scratch への退避（scratch.go）---
	ScratchAutoRelocate func(dir string)

	// --- 安定エラーコード（errcodes.go）---
	//
	// Console の i18n カタログと対になっている文字列。**gitx 側で定義し直さない**
	// —— 出所が 2 つになると、片方だけ直した日に画面が生のコードを出す。
	ErrCodeSessionsRunning       string
	ErrCodeSessionsRunningDelete string
	ErrCodeBranchInUse           string
	ErrCodeWorktreeDirty         string
	ErrCodeWorktreeRemoveFailed  string
	ErrCodeHasWorktrees          string
	ErrCodeLocked                string
	ErrCodeLockedSessions        string
}

var deps Deps

// Configure は起動時に 1 回だけ呼ぶ（main の git_wiring.go / gitx のテストの init）。
// 欠けたまま動かさない —— ロック判定やセッションの生存が「たまたま零値」で動くと、
// **消してはいけないものが消える**側に倒れる。
//
// 🔥 **網羅は reflect で取る。手書きの一覧にしない。** 手で並べた map はフィールドが
// 増えたときに漏れ、しかも漏れても何も起きない。危ないのは**値型**である: 関数型なら
// 未配線は nil 参照で落ちるが、`ErrCodeLocked` のような文字列は**空のまま静かに走り**、
// Console には `""` というコードが届く。この構造体は既に値型を 9 つ持っている。
//
// 例外を作るときは**フィールドに `gitx:"optional"` と書く**（一覧を別に持たない。
// 例外が見えるのは常に宣言のところ）。
func Configure(d Deps) {
	var missing []string
	v := reflect.ValueOf(d)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Tag.Get("gitx") == "optional" {
			continue
		}
		if unwired(v.Field(i)) {
			missing = append(missing, f.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		panic(fmt.Sprintf("gitx.Configure: 配線されていない依存がある: %v", missing))
	}
	deps = d
	gitHosts = d.GitHosts
	errCodeSessionsRunning = d.ErrCodeSessionsRunning
	errCodeSessionsRunningDelete = d.ErrCodeSessionsRunningDelete
	errCodeBranchInUse = d.ErrCodeBranchInUse
	errCodeWorktreeDirty = d.ErrCodeWorktreeDirty
	errCodeWorktreeRemoveFailed = d.ErrCodeWorktreeRemoveFailed
	errCodeHasWorktrees = d.ErrCodeHasWorktrees
	errCodeLocked = d.ErrCodeLocked
	errCodeLockedSessions = d.ErrCodeLockedSessions
}

// unwired は「配線されていない」の判定。零値に加えて、**中身の無いマップ**も
// 配線漏れとして扱う（`map[string]string{}` は零値ではないが、依存としては
// 未配線と同じ意味になる）。
func unwired(v reflect.Value) bool {
	if v.Kind() == reflect.Map {
		return v.Len() == 0
	}
	return v.IsZero()
}

// Wired は現在の配線を返す。**呼び出し側が「配線が生きているか」を通しで検査する**
// ための読み出し口で、gitx 自身は使わない。
//
// 🔥 Configure が捕まえるのは**未配線**だけで、**間違った配線**は捕まえられない。
// `RepoLocked` を「常に false」にしても静かに通る —— しかも配線は 1 行なので、
// 将来の整理で一番触られやすい場所である。
func Wired() Deps { return deps }

// 値で受け取るもの。Configure が 1 回だけ書く（以後は読むだけ）。
var (
	gitHosts                     map[string]string
	errCodeSessionsRunning       string
	errCodeSessionsRunningDelete string
	errCodeBranchInUse           string
	errCodeWorktreeDirty         string
	errCodeWorktreeRemoveFailed  string
	errCodeHasWorktrees          string
	errCodeLocked                string
	errCodeLockedSessions        string
)

// 以下は移送前と**同じ名前**の薄い委譲。移してきた 3,871 行を 1 行も触らずに済ませる
// ためで、ここが唯一の外向きの窓口になる。
func absPath(p string) string { return deps.AbsPath(p) }

func repoLocked(dir string) bool { return deps.RepoLocked(dir) }

func lockedRepoDirs() map[string]bool { return deps.LockedRepoDirs() }

func liveSessionsInDir(dir string) []string { return deps.LiveSessionsInDir(dir) }

func lockedSessionsInDir(metas []session.Meta, dir string) []string {
	return deps.LockedSessionsInDir(metas, dir)
}

func worktreeHasSessions(dir string) bool { return deps.WorktreeHasSessions(dir) }

func managedAlive(m session.Meta) bool { return deps.ManagedAlive(m) }

func finalizeSessionUsage(m session.Meta) { deps.FinalizeSessionUsage(m) }

func repoJobActive(name string) bool { return deps.RepoJobActive(name) }

func startRepoJob(kind, name, dir, url string, run func(ctx context.Context, sink RepoJobSink) error) any {
	return deps.StartRepoJob(kind, name, dir, url, run)
}

func isSvnRepo(dir string) bool { return deps.IsSvnRepo(dir) }

func svnRepoEntry(name, dir string) Repo { return deps.SvnRepoEntry(name, dir) }

func ensureCredHelper() error { return deps.EnsureCredHelper() }

func internalGitHost() string { return deps.InternalGitHost() }

func firstNonEmpty(vals ...string) string { return deps.FirstNonEmpty(vals...) }

func gitConfigGlobal(key, val string) error { return deps.GitConfigGlobal(key, val) }

func scratchAutoRelocate(dir string) { deps.ScratchAutoRelocate(dir) }

// 純粋な内部パッケージの薄皮は配線しない（振る舞いが無いので、写しが古くなる余地が
// 無い）。main 側の homeDir も同じ 1 行である。
func homeDir() string { return paths.HomeDir() }
