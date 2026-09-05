package chatx

// The reverse dependency chatx -> main is taken as an argument: one struct passed to Configure
// (ADR 0067 decisions 3 and 5). chatx cannot import main, so this is the only way.
//
// Why the struct has exported fields, and how that price is paid (the proviso in decision 5,
// #317). A Go composite literal compiles with named fields OMITTED, so as it stands a missing
// field is green. With a handful of dependencies the fix is unexported fields plus an N-argument
// constructor, letting the compiler count them - but there are 40 here, which makes that
// impractical. So this takes the same shape as internal/mcpx:
//
//   - `Configure` walks every field by reflection and panics if a single one is a zero value.
//   - `deps_test.go` drops the fields one at a time and checks that each one panics (a field
//     added later is covered automatically, even if the check is never updated).
//
// Never fill a gap silently with a default: an approval gate or a report destination left
// unwired and slipping through is worse than not starting at all (AG-BROWSER handover).

import (
	"reflect"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// ReplyMsg is one utterance used to build the reply-suggestion window. The same shape as
// main's replyMsg; the adapter in chat_wiring.go converts between them, because a main type
// cannot be named from chatx.
type ReplyMsg struct {
	Role string
	Text string
}

// Deps is everything that can only live on the main side. A field added here is picked up by
// Configure's reflection check automatically, so the check can never fall behind.
type Deps struct {
	// --- errcodes.go (frozen strings paired with the Console's err.<code> catalogue) ---
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

	// --- ui_prefs.go (accessors that depend on feature-side constants) ---
	AssistantAgentOrderPref func() []string
	AssistantChatModelPref  func(kind string) (string, bool)
	// Order and model for AI-assisted one-shot generation. Separate from chat: docs/log/84.
	AiAssistOrderPref func() []string
	AiShortModelPref  func(kind string) (string, bool)
	AiProseModelPref  func(kind string) (string, bool)
	ChatAutoTurnLimit func() int
	ChatAutoTurnModel func() string

	// --- model_deny.go ---
	FilterVisibleModels func(kind string, list []agents.ModelChoice) []agents.ModelChoice
	VisibleModel        func(kind, model string) string
	VisibleModelIDs     func(kind string, ids []string) []string

	// --- assistants.go (the //go:embed and the DI construction point stay in main) ---
	AssistantDeps          func() assistants.Deps
	EnsureBuiltinKnowledge func() string

	// --- session_title.go (title suggestion; shared with the session side) ---
	CleanSuggestedTitle      func(s string) string
	TitleModel               func() string
	TitleSuggestFooter       func(lang string) string
	TitleSuggestInstructions func(lang string) string
	TitleSuggestPersona      func(lang string) string
	TitleSuggestTimeout      time.Duration

	// --- session_suggest_reply.go (reply suggestion; shared with the session side) ---
	CleanSuggestedReplies    func(s string) []string
	ReplyCounterpartChat     int
	ReplySuggestEnabled      func() bool
	ReplySuggestInstructions func(lang string, counterpart int) string
	ReplySuggestLogHeader    func(lang string) string
	ReplySuggestModel        func() string
	ReplySuggestPersona      func(lang string) string
	ReplySuggestTimeout      time.Duration
	ReplySuggestWindow       func(b *strings.Builder, msgs []ReplyMsg)

	// --- one-offs ---
	AbortResumeHolds func(name string, a claude.Abort, now time.Time) bool
	ChatTurnUsageTag func(convID, seedVerb, trigger string) usagex.Tag
	CleanTitle       func(s string) (string, bool)
	NormalizeKind    func(kind string) string
	SafeBrowsePath   func(p string) (full, rel string, ok bool)
	// MaybePushOperatorReply pushes a reply out to the Discord/Slack bridge (bridge_operator.go).
	MaybePushOperatorReply func(conv, reply string)
	// RateLimitState reads the reservation of a rate-limit episode (the fstore handle in
	// rate_limit_resume.go). An accessor rather than the value, so the var is not copied: the
	// far side is a var, and receiving it into an alias variable makes a copy (hit twice in
	// wave A and once in wave B).
	RateLimitState func(name string) (scheduleID, resumeAt string, ok bool)
}

var deps Deps

// Configure is called exactly once at startup (main's chat_wiring.go, or the init of chatx's
// own tests). It panics if a single field is left at its zero value.
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
		panic("chatx.Configure: dependencies left unwired: " + strings.Join(missing, ", "))
	}
	deps = d
	bindValues(d)
}
