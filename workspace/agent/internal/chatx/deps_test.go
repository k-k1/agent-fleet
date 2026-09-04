package chatx

// The wiring chatx's own unit tests run on (production wiring lives in main's
// chat_wiring.go). Same shape as internal/mcpx/deps_test.go.
//
// The frozen wire values (errCode*) are the real strings: they pair with the Console's
// `err.<code>` catalogue and some tests assert on them. The functions are minimal
// implementations with the same shape as the real ones; no harmless defaults, because an
// unwired field is meant to make Configure panic.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

func testDeps() Deps {
	return Deps{
		ErrCodeChatAgentUnsupported:   "chat_agent_unsupported",
		ErrCodeChatAssistantNotFound:  "chat_assistant_not_found",
		ErrCodeChatConversationNotFnd: "chat_conversation_not_found",
		ErrCodeChatMessageEmpty:       "chat_message_empty",
		ErrCodeChatNothingToCompact:   "chat_nothing_to_compact",
		ErrCodeChatPromptEmpty:        "chat_prompt_empty",
		ErrCodeChatTitleEmpty:         "chat_title_empty",
		ErrCodeLocked:                 "locked",
		ErrCodeTitleFeatureDisabled:   "title_feature_disabled",
		ErrCodeTitleNoContent:         "title_no_content",

		// These read the preference exactly the way production does. A fake that returns
		// empty would leave the assertions untouched while the tests that check "the
		// setting takes effect" (TestResolveChatModelUsesAssistantPreference /
		// TestChatModelForResolvesPerActualBackend / TestChatAutoTurnLimit) stop passing
		// or stop measuring. internal/uiprefs owns the ui-prefs read, so the same path is
		// reachable without going through main.
		AssistantAgentOrderPref: func() []string { return DefaultHeadlessOrder },
		AssistantChatModelPref: func(kind string) (string, bool) {
			return modelPrefForTest("assistantModels", kind)
		},
		AiAssistOrderPref: func() []string { return DefaultHeadlessOrder },
		AiShortModelPref: func(kind string) (string, bool) {
			return modelPrefForTest("aiShortModels", kind)
		},
		AiProseModelPref: func(kind string) (string, bool) {
			return modelPrefForTest("aiProseModels", kind)
		},
		// Same clamping as main's chatAutoTurnLimit: the default when unset, always
		// within [1, the maximum].
		ChatAutoTurnLimit: func() int {
			v, ok := uiprefs.Read()["assistantAutoTurnLimit"].(float64)
			if !ok {
				return DefaultAutoTurns
			}
			n := int(v)
			if n < 1 {
				return 1
			}
			if n > MaxAutoTurnLimit {
				return MaxAutoTurnLimit
			}
			return n
		},
		ChatAutoTurnModel: func() string {
			v, _ := uiprefs.Read()["assistantAutoTurnModel"].(string)
			return strings.TrimSpace(v)
		},

		FilterVisibleModels: func(_ string, list []agents.ModelChoice) []agents.ModelChoice { return list },
		VisibleModel:        func(_, model string) string { return model },
		VisibleModelIDs:     func(_ string, ids []string) []string { return ids },

		AssistantDeps: func() assistants.Deps {
			return assistants.NewDeps(func() string { return "" }, func() string { return session.KindClaude })
		},
		// Create the same location main's ensureBuiltinKnowledge does
		// (chat_verb_test.go checks that a conversation carries this path in Knowledge).
		// Materializing the embedded body is main's job, so only the directory is made.
		EnsureBuiltinKnowledge: func() string {
			dir := filepath.Join(paths.HomeDir(), ".config", "agent-fleet", "knowledge", "af")
			_ = os.MkdirAll(dir, 0o700)
			return dir
		},

		CleanSuggestedTitle:      strings.TrimSpace,
		TitleModel:               func() string { return "haiku" },
		TitleSuggestFooter:       func(string) string { return "footer" },
		TitleSuggestInstructions: func(string) string { return "instructions" },
		TitleSuggestPersona:      func(string) string { return "persona" },
		TitleSuggestTimeout:      60 * time.Second,

		CleanSuggestedReplies:    func(s string) []string { return strings.Split(s, "\n") },
		ReplyCounterpartChat:     1,
		ReplySuggestEnabled:      func() bool { return true },
		ReplySuggestInstructions: func(string, int) string { return "reply-instructions" },
		ReplySuggestLogHeader:    func(string) string { return "log" },
		ReplySuggestModel:        func() string { return "haiku" },
		ReplySuggestPersona:      func(string) string { return "reply-persona" },
		ReplySuggestTimeout:      60 * time.Second,
		ReplySuggestWindow: func(b *strings.Builder, msgs []ReplyMsg) {
			for _, m := range msgs {
				b.WriteString(m.Role + ": " + m.Text + "\n")
			}
		},

		AbortResumeHolds: func(string, claude.Abort, time.Time) bool { return false },
		ChatTurnUsageTag: func(convID, seedVerb, trigger string) usagex.Tag {
			return usagex.Tag{Feature: usagex.FeatureAssistantChat, Trigger: trigger, Ref: convID, Verb: seedVerb}
		},
		CleanTitle: func(s string) (string, bool) {
			s = strings.TrimSpace(s)
			return s, s != ""
		},
		// Same shape as the real one (agent.go): an unknown kind folds to claude.
		NormalizeKind: func(kind string) string {
			if _, ok := ChatProviders[kind]; ok {
				return kind
			}
			return session.KindClaude
		},
		// Same shape as main's safeBrowsePath: only relative paths that stay under the
		// browse root get through. A pass-through fake would fail the test that an
		// attachment's location lands in Knowledge (chat_verb_test.go), because it never
		// resolves under the root — a weak fake breaks the check without touching a single
		// assertion. The deny list (isDenied) is fs.go's job.
		SafeBrowsePath: func(p string) (string, string, bool) {
			root := os.Getenv("AF_BROWSE_ROOT")
			if root == "" {
				root = paths.HomeDir()
			}
			p = strings.TrimSpace(p)
			clean := filepath.Clean(p)
			if filepath.IsAbs(p) {
				rel, err := filepath.Rel(root, clean)
				if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return "", "", false
				}
				if rel == "." {
					rel = ""
				}
				return clean, rel, true
			}
			if clean == "." {
				clean = ""
			}
			if clean == ".." || strings.HasPrefix(clean, "../") {
				return "", "", false
			}
			full := filepath.Join(root, clean)
			rel, err := filepath.Rel(root, full)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return "", "", false
			}
			if rel == "." {
				rel = ""
			}
			return full, rel, true
		},
		MaybePushOperatorReply: func(string, string) {},
		RateLimitState:         func(string) (string, string, bool) { return "", "", false },
	}
}

// modelPrefForTest reads the preference the way main's assistantModelPref does. Only the
// read is copied here; deciding the deny list is model_deny.go's job.
func modelPrefForTest(key, kind string) (string, bool) {
	raw, ok := uiprefs.Read()[key].(map[string]any)
	if !ok {
		return "", false
	}
	v, ok := raw[kind].(string)
	return v, ok
}

func init() { Configure(testDeps()) }

// TestConfigureRejectsEveryMissingField drops one field at a time and checks that Configure
// panics, for every field. A hand-written check grows a hole the moment a field is added
// and nobody remembers to extend it, so this walks the struct with reflect (the proviso to
// ADR decision 5, #317).
func TestConfigureRejectsEveryMissingField(t *testing.T) {
	full := testDeps()
	rt := reflect.TypeOf(full)
	t.Cleanup(func() { Configure(testDeps()) })
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		t.Run(name, func(t *testing.T) {
			d := testDeps()
			reflect.ValueOf(&d).Elem().Field(i).SetZero()
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("dropping %s did not panic (an unwired field would go green)", name)
				}
				if msg, _ := r.(string); !strings.Contains(msg, name) {
					t.Fatalf("the panic message does not mention %s: %v", name, r)
				}
			}()
			Configure(d)
		})
	}
}

// TestInstallReconcilerForTestHasNoProductionCaller pins that the seam exported for tests
// only is never called from production code (review note 3).
//
// `InstallReconcilerForTest` is exported because the three `TestSessionReport*`
// end-to-end tests rely on the real reconciler running (`reportRec` is a var, and taking
// it under another name yields a copy the swap never reaches). It IS callable from
// production, so a first caller has to show up as a red test.
//
// It looks at CALLS through the AST rather than searching text: a plain string match also
// hits the declaring file and the doc comment, and would be red forever.
// It also checks how many files it scanned (the #320 "finds nothing, so checks nothing"
// failure): if a path assumption breaks and it reads zero files, the check reports "no
// callers" unconditionally, which is the same as not existing.
func TestInstallReconcilerForTestHasNoProductionCaller(t *testing.T) {
	roots := []string{".", "../.."} // internal/chatx and workspace/agent (package main)
	scanned := 0
	for _, root := range roots {
		ents, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("cannot scan %s: %v", root, err)
		}
		for _, e := range ents {
			n := e.Name()
			if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
				continue
			}
			path := filepath.Join(root, n)
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			scanned++
			ast.Inspect(f, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := ""
				switch fn := call.Fun.(type) {
				case *ast.Ident:
					name = fn.Name
				case *ast.SelectorExpr:
					name = fn.Sel.Name
				}
				if name == "InstallReconcilerForTest" {
					t.Errorf("%s calls InstallReconcilerForTest"+
						" (a test-only seam; if production must drive it, revisit the design)", path)
				}
				return true
			})
		}
	}
	if scanned < 50 {
		t.Fatalf("only %d non-test .go files were read = this check has gone silent"+
			" (did the layout move? roots=%v)", scanned, roots)
	}
}
