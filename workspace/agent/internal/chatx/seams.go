package chatx

// Exposes the main-side implementations received through deps.go in a shape the family's
// code can call under their original spellings, so moving the code in needs no edit beyond
// changing the package line. (Same shape as the lower half of internal/mcpx/deps.go.)
//
// Values that came from consts are package variables Configure fills, not functions:
// functions would mean adding `()` at every call site, weakening the claim that this was a
// pure move. Written exactly once, by Configure.

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

// --- Resolvable without going through main (deliberately not in Deps) --------

func homeDir() string { return paths.HomeDir() }

// envOr reads an environment variable with a default. The same three lines exist in
// main.go and internal/agents/copilot; as copied-pure-function debt they are what a
// reclaim wave is meant to unify. RECLAIM-B folded the CP side into internal/envx; the
// agent side is left alone here because main.go has no owner.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// titleLang is the language a suggested subject line is generated in; it follows the
// display language (ADR 0016).
func titleLang() string { return uiprefs.Locale() }

// --- Values Configure fills (consts in main originally) ----------------------

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

// --- Seams onto the functions Configure received -----------------------------

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

// chatTurnUsageTag is the usage tag for one conversation turn. ChatConversation is a type
// on this side, so the seam passes plain values only and main's tag assembly stays put.
func chatTurnUsageTag(c *ChatConversation, trigger string) usagex.Tag {
	return deps.ChatTurnUsageTag(c.ID, c.SeedVerb, trigger)
}
