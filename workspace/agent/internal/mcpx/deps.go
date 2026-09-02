package mcpx

// deps.go — mcpx が呼び出し側（package main）へ伸ばしている手を 1 枚に集めたもの。
//
// MCP の stdio サーバは、エージェントの機能面をほぼ丸ごと道具として出す面である
// （セッション作成・ピア送信・完了報告・承認ゲート・UI 設定・ツール版ピン…）。
// だから家系を切り出すと、**外向きの依存はどうしても main の各家系へ散る**。
// ここはその断面を隠すのではなく、**1 箇所に集めて数えられるようにする**ための口:
//
//   - mcpx は main を import しない（できない。逆向きの依存が既にある）
//   - なので「main の関数を呼ぶ」は関数値として受け取る形にする
//   - **配線は起動時に 1 回**（main の alias_mcp.go の init）。Configure が
//     欠けを検査して落とす —— 配線漏れを既定値で黙って埋めると、承認ゲートが
//     素通りするような穴になるので、**静かに動くより落ちる方を選ぶ**
//
// mcpx 単体のテストは main を持たないので、TestMain が偽物を配線する（deps_test 参照）。

import (
	"fmt"
	"net/http"
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Deps は上のとおり「mcpx から見た外の世界」。**型は main のものを 1 つも含まない**
// （含んだ瞬間に切断面が閉じなくなる）ので、増えても import は増えない。
type Deps struct {
	// セッション件名（session.go）。**上限を mcpx 側で持ち直さない** —— 層ごとに違う
	// 数字を持つと「起動の瞬間だけ落ちる」形の事故になる（memory:
	// session-title-limit-one-source）。
	CleanTitle           func(s string) (string, bool)
	SessionTitleMaxRunes int

	// セッション間メッセージ（session_peer.go）。
	PeerIntentNames       []string
	PeerReachableSessions func(from string) []session.Meta

	// 完了報告の種別（chat_report.go）。
	ReportKindSelfReport string

	// オペレーター承認（bridge_approval.go）。**既定値を置かない**筆頭がこれで、
	// 未配線を「素通り」にすると承認そのものが消える。
	ApprovalGate      func(op, summary string) error
	ApprovalLabel     func(op string) string
	ShellCreateTarget func(dir, prompt string) string
	ShellSendTarget   func(name, prompt string) string
	SessionIsShell    func(name string) bool

	// 画面設定（ui_prefs.go）・claude の設定配線（session_status.go）。
	ReadUIPrefs                func() map[string]any
	EnsureClaudeSettingsWiring func(kind string)

	// リポジトリ解決（git.go）: HTTP の path からワークツリーを引く。
	RepoAnyDirFromPath func(w http.ResponseWriter, r *http.Request) (string, bool)

	// ツールの版ピンと導入（env_tool_versions.go / install_tools.go）。
	ReadBuildPins      func() map[string]string
	AgentFleetShareDir func() string
	InstallGrafanaMCP  func(ver string) (string, error)

	// SSM セッションの設定書き出し（session_ssm.go）。
	WriteSSMConfig func(path string, s session.SSMMeta) error

	// --- 道具集合を決める 3 つのフラグ ---
	//
	// 🔥 **保管場所は呼び出し側に置く。** package main のテストが
	// `mcpWriteEnabled = true` のように**直接代入して**道具集合を切り替えており、
	// var のエイリアスで受けると代入が mcpx まで届かず、**スタブが効かないのに緑**に
	// なる（ウェーブ A の #295 F-2 / #297 F-1 で 2 回踏んだ形）。読み書きの口だけを
	// ここで預かり、値は 1 箇所にしか置かない。
	//
	//   WriteEnabled          … `--write`。書き込み系の道具を出すか（アシスタント面）
	//   SelfReportOnly        … `--self-report`。セッション面（自己申告だけの狭い面）
	//   SessionChromiumEnabled… `--self-report --chromium-attach` の同時指定でだけ立つ。
	//                            単独の --chromium-attach でアシスタント面が広がらない
	//                            ように、連言はここではなく RunStdio が決める
	WriteEnabled              func() bool
	SetWriteEnabled           func(bool)
	SelfReportOnly            func() bool
	SetSelfReportOnly         func(bool)
	SessionChromiumEnabled    func() bool
	SetSessionChromiumEnabled func(bool)

	// 会話 id（`--conv <id>`）。これも保管は呼び出し側である。
	// 🔥 **var のエイリアスでは渡せない値**の典型: main が起動時に写しを取っても、
	// この id は**そのあと**（引数解釈のとき）に決まるので写しは空文字のまま固まり、
	// bridge_approval.go は「どの会話か分からない」まま承認を投げることになる。
	// しかも main 側のテストは解釈**結果**（conv が入ったか）を読むので、
	// 一方向の通知では足りない —— 読み書き両方をここで預かる。
	ConvID    func() string
	SetConvID func(id string)
}

var deps Deps

// Configure は起動時に 1 回だけ呼ぶ（main の alias_mcp.go / mcpx のテストの TestMain）。
// 欠けたまま動かさない —— 承認ゲートやセッション件名の上限が「たまたま零値」で動くと、
// 壊れていることが誰にも見えない形の穴になる。
func Configure(d Deps) {
	var missing []string
	req := map[string]bool{
		"CleanTitle":                 d.CleanTitle == nil,
		"SessionTitleMaxRunes":       d.SessionTitleMaxRunes <= 0,
		"PeerIntentNames":            len(d.PeerIntentNames) == 0,
		"PeerReachableSessions":      d.PeerReachableSessions == nil,
		"ReportKindSelfReport":       d.ReportKindSelfReport == "",
		"ApprovalGate":               d.ApprovalGate == nil,
		"ApprovalLabel":              d.ApprovalLabel == nil,
		"ShellCreateTarget":          d.ShellCreateTarget == nil,
		"ShellSendTarget":            d.ShellSendTarget == nil,
		"SessionIsShell":             d.SessionIsShell == nil,
		"ReadUIPrefs":                d.ReadUIPrefs == nil,
		"EnsureClaudeSettingsWiring": d.EnsureClaudeSettingsWiring == nil,
		"RepoAnyDirFromPath":         d.RepoAnyDirFromPath == nil,
		"ReadBuildPins":              d.ReadBuildPins == nil,
		"AgentFleetShareDir":         d.AgentFleetShareDir == nil,
		"InstallGrafanaMCP":          d.InstallGrafanaMCP == nil,
		"WriteSSMConfig":             d.WriteSSMConfig == nil,
		"WriteEnabled":               d.WriteEnabled == nil || d.SetWriteEnabled == nil,
		"SelfReportOnly":             d.SelfReportOnly == nil || d.SetSelfReportOnly == nil,
		"SessionChromiumEnabled":     d.SessionChromiumEnabled == nil || d.SetSessionChromiumEnabled == nil,
		"ConvID":                     d.ConvID == nil || d.SetConvID == nil,
	}
	for name, bad := range req {
		if bad {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		panic(fmt.Sprintf("mcpx.Configure: 配線されていない依存がある: %v", missing))
	}
	deps = d
	sessionTitleMaxRunes = d.SessionTitleMaxRunes
	peerIntentNames = d.PeerIntentNames
	reportKindSelfReport = d.ReportKindSelfReport
}

// 値で受け取るもの。Configure が 1 回だけ書く（以後は読むだけ）。
var (
	sessionTitleMaxRunes int
	peerIntentNames      []string
	reportKindSelfReport string
)

// 以下は移送前と**同じ名前**の薄い委譲。呼び出し側（移してきた 3,461 行）を 1 行も
// 触らずに済ませるためで、ここが唯一の外向きの窓口になる。
func cleanTitle(s string) (string, bool) { return deps.CleanTitle(s) }

func peerReachableSessions(from string) []session.Meta { return deps.PeerReachableSessions(from) }

func bridgeApprovalGate(op, summary string) error { return deps.ApprovalGate(op, summary) }

func approvalLabel(op string) string { return deps.ApprovalLabel(op) }

func shellCreateTarget(dir, prompt string) string { return deps.ShellCreateTarget(dir, prompt) }

func shellSendTarget(name, prompt string) string { return deps.ShellSendTarget(name, prompt) }

func sessionIsShell(name string) bool { return deps.SessionIsShell(name) }

func readUIPrefs() map[string]any { return deps.ReadUIPrefs() }

func ensureClaudeSettingsWiring(kind string) { deps.EnsureClaudeSettingsWiring(kind) }

func repoAnyDirFromPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	return deps.RepoAnyDirFromPath(w, r)
}

func readBuildPins() map[string]string { return deps.ReadBuildPins() }

func agentFleetShareDir() string { return deps.AgentFleetShareDir() }

func installGrafanaMCP(ver string) (string, error) { return deps.InstallGrafanaMCP(ver) }

func writeSSMConfig(path string, s session.SSMMeta) error { return deps.WriteSSMConfig(path, s) }

// 道具集合のフラグ（保管は呼び出し側・上の Deps の注記を参照）。
func writeEnabled() bool           { return deps.WriteEnabled() }
func selfReportOnly() bool         { return deps.SelfReportOnly() }
func sessionChromiumEnabled() bool { return deps.SessionChromiumEnabled() }

func setWriteEnabled(v bool)           { deps.SetWriteEnabled(v) }
func setSelfReportOnly(v bool)         { deps.SetSelfReportOnly(v) }
func setSessionChromiumEnabled(v bool) { deps.SetSessionChromiumEnabled(v) }

func convID() string      { return deps.ConvID() }
func setConvID(id string) { deps.SetConvID(id) }

// 純粋な標準ライブラリの薄皮は配線しない（振る舞いが無いので、写しが古くなる余地が無い）。
func homeDir() string { return paths.HomeDir() }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
