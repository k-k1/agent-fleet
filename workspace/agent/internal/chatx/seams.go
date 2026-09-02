package chatx

// deps.go で受け取った main 側の実体を、**家系のコードが元の綴りのまま呼べる形**に開く。
// ここを挟むことで、移送は「package 行を変える」以上の書き換えを家系側へ持ち込まない。
// （internal/mcpx/deps.go の下半分と同じ形。）
//
// 値（const 由来）は Configure が埋める package 変数にしてある——関数にすると呼び出し側に
// `()` を足して回ることになり、純粋移送の主張が弱くなるため。**書き込みは Configure の 1 回だけ。**

import (
	"os"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// --- main を介さずに解けるもの（Deps に入れない）-----------------------------

func homeDir() string { return paths.HomeDir() }

// envOr は環境変数の既定値つき読み出し。main.go / internal/agents/copilot にも同じ 3 行があり、
// 「写した純関数」の債務として回収ウェーブが 1 本化する対象（RECLAIM-B で CP 側は internal/envx へ
// 畳んだ。agent 側は main.go が無所有なので今回は触らない）。
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// titleLang は件名提案の言語（表示言語に従う・ADR 0016）。
func titleLang() string { return uiprefs.Locale() }

// --- Configure が埋める値（元は main の const）-------------------------------

var (
	errCodeChatAgentUnsupported   string
	errCodeChatAssistantNotFound  string
	errCodeChatConversationNotFnd string
	errCodeChatMessageEmpty       string
	errCodeChatNothingToCompact   string
	errCodeChatPromptEmpty        string
	errCodeChatTitleEmpty         string
	errCodeLocked                 string
	errCodeTitleFeatureDisabled   string
	errCodeTitleNoContent         string

	replyCounterpartChat int
	replySuggestTimeout  time.Duration
	titleSuggestTimeout  time.Duration
)

func bindValues(d Deps) {
	errCodeChatAgentUnsupported = d.ErrCodeChatAgentUnsupported
	errCodeChatAssistantNotFound = d.ErrCodeChatAssistantNotFound
	errCodeChatConversationNotFnd = d.ErrCodeChatConversationNotFnd
	errCodeChatMessageEmpty = d.ErrCodeChatMessageEmpty
	errCodeChatNothingToCompact = d.ErrCodeChatNothingToCompact
	errCodeChatPromptEmpty = d.ErrCodeChatPromptEmpty
	errCodeChatTitleEmpty = d.ErrCodeChatTitleEmpty
	errCodeLocked = d.ErrCodeLocked
	errCodeTitleFeatureDisabled = d.ErrCodeTitleFeatureDisabled
	errCodeTitleNoContent = d.ErrCodeTitleNoContent

	replyCounterpartChat = d.ReplyCounterpartChat
	replySuggestTimeout = d.ReplySuggestTimeout
	titleSuggestTimeout = d.TitleSuggestTimeout
}

// --- Configure が受け取った関数への継ぎ目 ------------------------------------

func assistantAgentOrderPref() []string { return deps.AssistantAgentOrderPref() }
func aiAssistOrderPref() []string       { return deps.AiAssistOrderPref() }
func assistantChatModelPref(kind string) (string, bool) {
	return deps.AssistantChatModelPref(kind)
}
func aiShortModelPref(kind string) (string, bool) { return deps.AiShortModelPref(kind) }
func aiProseModelPref(kind string) (string, bool) { return deps.AiProseModelPref(kind) }
func chatAutoTurnLimit() int                      { return deps.ChatAutoTurnLimit() }
func chatAutoTurnModel() string                   { return deps.ChatAutoTurnModel() }

func filterVisibleModels(kind string, list []agents.ModelChoice) []agents.ModelChoice {
	return deps.FilterVisibleModels(kind, list)
}
func visibleModel(kind, model string) string             { return deps.VisibleModel(kind, model) }
func visibleModelIDs(kind string, ids []string) []string { return deps.VisibleModelIDs(kind, ids) }

func assistantDeps() assistants.Deps { return deps.AssistantDeps() }
func ensureBuiltinKnowledge() string { return deps.EnsureBuiltinKnowledge() }

func cleanSuggestedTitle(s string) string         { return deps.CleanSuggestedTitle(s) }
func titleModel() string                          { return deps.TitleModel() }
func titleSuggestFooter(lang string) string       { return deps.TitleSuggestFooter(lang) }
func titleSuggestInstructions(lang string) string { return deps.TitleSuggestInstructions(lang) }
func titleSuggestPersona(lang string) string      { return deps.TitleSuggestPersona(lang) }

func cleanSuggestedReplies(s string) []string { return deps.CleanSuggestedReplies(s) }
func replySuggestEnabled() bool               { return deps.ReplySuggestEnabled() }
func replySuggestInstructions(lang string, counterpart int) string {
	return deps.ReplySuggestInstructions(lang, counterpart)
}
func replySuggestLogHeader(lang string) string { return deps.ReplySuggestLogHeader(lang) }
func replySuggestModel() string                { return deps.ReplySuggestModel() }
func replySuggestPersona(lang string) string   { return deps.ReplySuggestPersona(lang) }
func replySuggestWindow(b *strings.Builder, msgs []ReplyMsg) {
	deps.ReplySuggestWindow(b, msgs)
}

func abortResumeHolds(name string, a claude.Abort, now time.Time) bool {
	return deps.AbortResumeHolds(name, a, now)
}
func cleanTitle(s string) (string, bool) { return deps.CleanTitle(s) }
func normalizeKind(kind string) string   { return deps.NormalizeKind(kind) }
func safeBrowsePath(p string) (full, rel string, ok bool) {
	return deps.SafeBrowsePath(p)
}
func maybePushOperatorReply(conv, reply string) { deps.MaybePushOperatorReply(conv, reply) }

// chatTurnUsageTag は会話 1 ターン分のタグ。chatConversation はこちら側の型なので、
// 継ぎ目は**素の値だけ**を渡す（main のタグ組み立てはそのまま）。
func chatTurnUsageTag(c *ChatConversation, trigger string) usagex.Tag {
	return deps.ChatTurnUsageTag(c.ID, c.SeedVerb, trigger)
}
