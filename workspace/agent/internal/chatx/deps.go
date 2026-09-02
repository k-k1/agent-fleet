package chatx

// chatx → main の逆向き依存を **引数（Configure に渡す 1 個の struct）で受け取る**
// （ADR 0067 決定 3・決定 5）。chatx は main を import できないので、これが唯一の方法。
//
// 🔥 **公開フィールドの struct にした理由と、その代償の埋め方**（ADR 決定 5 の但し書き・#317）。
// Go の複合リテラルは名前付きフィールドを**省略してもコンパイルが通る**ので、
// このままだと「書き落としても緑」になる。依存が数個なら非公開フィールド＋引数 N 個の
// コンストラクタで数をコンパイラに数えさせるが、**ここは 40 個あって現実的でない**。
// そこで internal/mcpx と同じ形を採る:
//
//   - `Configure` が **reflect で全フィールドを走査**し、ゼロ値が 1 つでもあれば **panic**。
//   - `deps_test.go` が **フィールドを 1 つずつ落として panic することを確かめる**
//     （フィールドを足したときに検査へ足し忘れても、reflect が自動で見る）。
//
// **既定値で黙って埋めない** — 承認ゲートや報告の宛先が未配線のまま素通りする方が、
// 起動しないことより悪い（AG-BROWSER の申し送り）。

import (
	"reflect"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// ReplyMsg は返信サジェストの窓を組むための「1 発言」。main の replyMsg と同じ形で、
// 変換は chat_wiring.go のアダプタが行う（main の型を chatx から名指しできないため）。
type ReplyMsg struct {
	Role string
	Text string
}

// Deps は main 側にしか置けないものの集合。**フィールドを足したら Configure の
// reflect 検査が自動で見る**ので、検査側に足し忘れることはない。
type Deps struct {
	// --- errcodes.go（Console の err.<code> カタログと対の凍結文字列）---
	ErrCodeChatAgentUnsupported   string
	ErrCodeChatAssistantNotFound  string
	ErrCodeChatConversationNotFnd string
	ErrCodeChatMessageEmpty       string
	ErrCodeChatNothingToCompact   string
	ErrCodeChatPromptEmpty        string
	ErrCodeChatTitleEmpty         string
	ErrCodeLocked                 string
	ErrCodeTitleFeatureDisabled   string
	ErrCodeTitleNoContent         string

	// --- ui_prefs.go（機能側の定数に依存するアクセサ）---
	AssistantAgentOrderPref   func() []string
	AssistantChatModelPref    func(kind string) (string, bool)
	AssistantUtilityModelPref func(kind string) (string, bool)
	ChatAutoTurnLimit         func() int
	ChatAutoTurnModel         func() string

	// --- model_deny.go ---
	FilterVisibleModels func(kind string, list []agents.ModelChoice) []agents.ModelChoice
	VisibleModel        func(kind, model string) string
	VisibleModelIDs     func(kind string, ids []string) []string

	// --- assistants.go（//go:embed と DI の構築点が main に残っている）---
	AssistantDeps          func() assistants.Deps
	EnsureBuiltinKnowledge func() string

	// --- session_title.go（件名提案。session 側と共有）---
	CleanSuggestedTitle      func(s string) string
	TitleModel               func() string
	TitleSuggestFooter       func(lang string) string
	TitleSuggestInstructions func(lang string) string
	TitleSuggestPersona      func(lang string) string
	TitleSuggestTimeout      time.Duration

	// --- session_suggest_reply.go（返信サジェスト。session 側と共有）---
	CleanSuggestedReplies    func(s string) []string
	ReplyCounterpartChat     int
	ReplySuggestEnabled      func() bool
	ReplySuggestInstructions func(lang string, counterpart int) string
	ReplySuggestLogHeader    func(lang string) string
	ReplySuggestModel        func() string
	ReplySuggestPersona      func(lang string) string
	ReplySuggestTimeout      time.Duration
	ReplySuggestWindow       func(b *strings.Builder, msgs []ReplyMsg)

	// --- その他 1 本ずつ ---
	AbortResumeHolds func(name string, a claude.Abort, now time.Time) bool
	ChatTurnUsageTag func(convID, seedVerb, trigger string) usagex.Tag
	CleanTitle       func(s string) (string, bool)
	NormalizeKind    func(kind string) string
	SafeBrowsePath   func(p string) (full, rel string, ok bool)
	// MaybePushOperatorReply は Discord/Slack ブリッジへ返信を押し出す（bridge_operator.go）。
	MaybePushOperatorReply func(conv, reply string)
	// RateLimitState は上限エピソードの予約を読む（rate_limit_resume.go の fstore ハンドル）。
	// **var を写さないため、値ではなく読み口を受ける** — 遠側は var なので、エイリアス変数で
	// 受けると写しになる（ウェーブ A で 2 回、ウェーブ B で 1 回踏んだ形）。
	RateLimitState func(name string) (scheduleID, resumeAt string, ok bool)
}

var deps Deps

// Configure は起動時に 1 回だけ呼ぶ（main の chat_wiring.go / chatx のテストの init）。
// **ゼロ値のフィールドが 1 つでもあれば panic** する。
func Configure(d Deps) {
	v := reflect.ValueOf(d)
	t := v.Type()
	var missing []string
	for i := 0; i < t.NumField(); i++ {
		if v.Field(i).IsZero() {
			missing = append(missing, t.Field(i).Name)
		}
	}
	if len(missing) > 0 {
		panic("chatx.Configure: 未配線のフィールド: " + strings.Join(missing, ", "))
	}
	deps = d
	bindValues(d)
}
