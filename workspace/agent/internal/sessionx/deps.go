package sessionx

// deps.go — sessionx が呼び出し側（package main）へ伸ばしている手を 1 枚に集めたもの。
//
// session 家系は「Console から見た製品そのもの」なので、外向きの依存はごく少数の
// 一般ヘルパと、家系の外に居座る 2 つの状態（MCP の会話 ID・オペレーターのターン）に
// 収束する。ここはその断面を隠すのではなく、**1 箇所に集めて数えられるようにする**
// ための口である（internal/gitx/deps.go・internal/memoryx/deps.go と同じ形）:
//
//   - sessionx は main を import しない（できない。逆向きの依存が既にある）
//   - なので「main の関数を呼ぶ」は関数値として受け取る形にする
//   - **配線は起動時に 1 回**（main の session_wiring.go の init）。Configure が欠けを
//     検査して落とす —— 配線漏れを既定値で黙って埋めると、たとえば
//     `MaxUploadBytes` が 0 になって**あらゆるアップロードが「大きすぎます」で落ちる**、
//     `ErrCodeLocked` が空になって**Console に生のコードが出る**。静かに動くより
//     落ちる方を選ぶ。
//
// 🔥 **エラーコードは sessionx 側で定義し直さない。** 出所が 2 つになると、片方だけ
// 直した日に画面が生のコードを出す。`errcodes.go` は agent 全体で 15 ファイルが引く
// 横断表なので **package main に残す**のが正しく、ここへは値として渡す
// （gitx / memoryx と同じ扱い）。
//
// sessionx 単体のテストは main を持たないので、TestMain ではなく init が偽物を配線する
// （deps_test.go 参照）。

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Deps は「sessionx から見た外の世界」。**型は main のものを 1 つも含まない**
// （含んだ瞬間に切断面が閉じなくなる）ので、増えても import は増えない。
type Deps struct {
	// --- 一般ヘルパ（main.go / connections.go / repo_prompts.go）---
	//
	// どれも「main のどこかに 1 本だけ在る」種類の関数で、写しを持つと
	// 片方だけ直った日に無言でずれる（README §0.5「原本と写し」）。
	EnvOr            func(key, def string) string
	FirstNonEmpty    func(vals ...string) string
	SplitFrontmatter func(s string) (map[string]string, string)

	// --- ファイル面（fs.go）---
	//
	// BrowseRoot は添付・貼り付け画像の保存先の根。MaxUploadBytes は上限バイト数で、
	// **未配線の 0 は「上限 0 バイト」＝全部拒否**になるので零値を許さない。
	BrowseRoot     func() string
	MaxUploadBytes func() int64

	// --- リポジトリ面（svn.go / repo_jobs.go）---
	//
	// セッション一覧はワークツリーの状態を併記するので、git 以外（svn）と
	// 取り込みジョブの走行数を家系の外から引く。
	IsSvnRepo       func(dir string) bool
	RepoJobsRunning func() int

	// --- 使用量の締め（usage_fold.go）---
	//
	// セッションの停止・削除で使用量台帳を締める。**usage_fold.go は main に残す**
	// —— 締めの駆動は `usage_ledger_test.go` / `usage_dedup_test.go`（主題が
	// main の usage_ledger.go）が握っており、家系へ引き込むと台帳のテストが
	// 台帳から離れる。gitx も同じ関数を継ぎ目で受けている（gitx/deps.go）。
	FinalizeSessionUsage  func(m session.Meta)
	MaybeFoldSessionUsage func()

	// --- 端末履歴（terminal_history.go）---
	//
	// セッションの後始末で履歴ファイルを消す。**未配線を no-op で埋めると
	// 消し忘れが無言で残る**ので、ここも零値を許さない。
	RemoveTerminalHistory func(name string)

	// --- ツールチェーン（env_toolchains.go）---
	ToolchainShellPrefix func() string

	// --- ブリッジ／オペレーター（mcp_wiring.go / bridge_operator.go）---
	//
	// 🔥 MCPConvID は **var を関数で受ける**。main 側の `mcpConvID` は
	// `mcp_wiring.go` が実行中に書き換える可変状態なので、値で受けると
	// **配線した瞬間の値で固まり、承認プロンプトが常に古い会話へ飛ぶ**。
	// （README の `var usageMu = usagex.Mu` と同じ「写してはいけない遠側」の形。
	// ここは錠ではないので vet は鳴らない —— だから明示的に関数で受ける。）
	MCPConvID       func() string
	RunOperatorTurn func(conv, text string) (string, error)

	// --- 安定エラーコード（errcodes.go）---
	//
	// Console の i18n カタログ（console/src/core/api/client.ts の ERR_TEXT）と対に
	// なっている文字列。**sessionx 側で定義し直さない。**
	// 恒久要因（未ログイン／未接続）で共有 daemon を起こさなかったとき。runtime_failed
	// （一時的な失敗）と分かれているのは、Console の文言も isTransientErr の判定も
	// 「待てば直るか」で変わるため（runtime_err.go）。
	ErrCodeAgentNotConnected      string
	ErrCodeChatConversationNotFnd string
	ErrCodeForkAtUnsupported      string
	ErrCodeForkBadAnchor          string
	ErrCodeForkMissingDir         string
	ErrCodeForkUnsupportedKind    string
	ErrCodeLocked                 string
	ErrCodePasteTooLarge          string
	ErrCodePasteUnsupportedAgent  string
	ErrCodePasteUnsupportedKind   string
	ErrCodeTitleFeatureDisabled   string
	ErrCodeTitleNoContent         string
}

var deps Deps

// Configure は起動時に 1 回だけ呼ぶ（main の session_wiring.go / sessionx のテストの init）。
// 欠けたまま動かさない。
//
// 🔥 **網羅は reflect で取る。手書きの一覧にしない。** 手で並べた map はフィールドが
// 増えたときに漏れ、しかも漏れても何も起きない。危ないのは**値型**である: 関数型なら
// 未配線は nil 参照で落ちるが、`ErrCodeLocked` のような文字列は**空のまま静かに走り**、
// Console には `""` というコードが届く。この構造体は既に値型を 12 個持っている。
//
// 例外を作るときは**フィールドに `sessionx:"optional"` と書く**（一覧を別に持たない。
// 例外が見えるのは常に宣言のところ）。
func Configure(d Deps) {
	var missing []string
	v := reflect.ValueOf(d)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Tag.Get("sessionx") == "optional" {
			continue
		}
		if v.Field(i).IsZero() {
			missing = append(missing, f.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		panic(fmt.Sprintf("sessionx.Configure: 配線されていない依存がある: %v", missing))
	}
	deps = d
	errCodeAgentNotConnected = d.ErrCodeAgentNotConnected
	errCodeChatConversationNotFnd = d.ErrCodeChatConversationNotFnd
	errCodeForkAtUnsupported = d.ErrCodeForkAtUnsupported
	errCodeForkBadAnchor = d.ErrCodeForkBadAnchor
	errCodeForkMissingDir = d.ErrCodeForkMissingDir
	errCodeForkUnsupportedKind = d.ErrCodeForkUnsupportedKind
	errCodeLocked = d.ErrCodeLocked
	errCodePasteTooLarge = d.ErrCodePasteTooLarge
	errCodePasteUnsupportedAgent = d.ErrCodePasteUnsupportedAgent
	errCodePasteUnsupportedKind = d.ErrCodePasteUnsupportedKind
	errCodeTitleFeatureDisabled = d.ErrCodeTitleFeatureDisabled
	errCodeTitleNoContent = d.ErrCodeTitleNoContent
}

// Wired は現在の配線を返す。**呼び出し側が「配線が生きているか」を通しで検査する**
// ための読み出し口で、sessionx 自身は使わない。
//
// 🔥 Configure が捕まえるのは**未配線**だけで、**間違った配線**は捕まえられない。
// とくに 12 本のエラーコードは**全部同じ `string` 型**なので、2 つ入れ替えても
// 型検査も reflect の網羅検査も鳴らない（2026-09-03 に独立 3 例が出た形）。
// そこは main 側の session_wiring_test.go が本物の定数と突き合わせて止める。
func Wired() Deps { return deps }

// 値で受け取るもの。Configure が 1 回だけ書く（以後は読むだけ）。
var (
	errCodeAgentNotConnected      string
	errCodeChatConversationNotFnd string
	errCodeForkAtUnsupported      string
	errCodeForkBadAnchor          string
	errCodeForkMissingDir         string
	errCodeForkUnsupportedKind    string
	errCodeLocked                 string
	errCodePasteTooLarge          string
	errCodePasteUnsupportedAgent  string
	errCodePasteUnsupportedKind   string
	errCodeTitleFeatureDisabled   string
	errCodeTitleNoContent         string
)

// 以下は移送前と**同じ名前**の薄い委譲。移してきた 12,393 行を 1 行も触らずに済ませる
// ためで、ここが唯一の外向きの窓口になる。
func envOr(key, def string) string { return deps.EnvOr(key, def) }

func firstNonEmpty(vals ...string) string { return deps.FirstNonEmpty(vals...) }

func splitFrontmatter(s string) (map[string]string, string) { return deps.SplitFrontmatter(s) }

func browseRoot() string { return deps.BrowseRoot() }

func maxUploadBytes() int64 { return deps.MaxUploadBytes() }

func isSvnRepo(dir string) bool { return deps.IsSvnRepo(dir) }

func repoJobsRunning() int { return deps.RepoJobsRunning() }

func removeTerminalHistory(name string) { deps.RemoveTerminalHistory(name) }

func finalizeSessionUsage(m session.Meta) { deps.FinalizeSessionUsage(m) }

func maybeFoldSessionUsage() { deps.MaybeFoldSessionUsage() }

func toolchainShellPrefix() string { return deps.ToolchainShellPrefix() }

func runOperatorTurn(conv, text string) (string, error) { return deps.RunOperatorTurn(conv, text) }

// mcpConvID は main 側では**変数**だった（mcp_wiring.go が実行中に書き換える）。
// パッケージを跨いで変数は共有できないので、ここだけは「変数の読み」が
// 「関数の呼び出し」に変わる —— bridge_approval.go の 3 箇所が `mcpConvID()` になる。
// **値で写すと承認プロンプトが常に古い会話へ飛ぶ**ので、この 1 段は省けない。
func mcpConvID() string { return deps.MCPConvID() }

// 純粋な内部パッケージの薄皮は配線しない（振る舞いが無いので、写しが古くなる余地が
// 無い）。main 側の homeDir も同じ 1 行である。
func homeDir() string { return paths.HomeDir() }
