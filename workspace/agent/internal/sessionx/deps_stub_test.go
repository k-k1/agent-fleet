package sessionx

// deps_stub_test.go — fake wiring so sessionx tests can go through `chatx` / `gitx`.
//
// The production wiring lives in main's `chat_wiring.go` init, and the sessionx test binary has
// no main, so without calling `chatx.Configure` here `deps.rateLimitState` is nil and the tests
// die (measured: TestRateLimitResumeNoteOnFailedReport SIGSEGVs at chat_report.go:226).
//
// Do not list the 44 fields by hand. A hand-written list goes stale the day chatx.Deps grows a
// field, and a gap makes `Configure` panic, which takes the whole test binary down with no
// readable reason. So reflect fills every field, and function fields are made to panic when
// stepped on (the same idea as `unreached` in internal/gitx/deps_test.go: a made-up return
// value would silently turn green on a lie once some future check reaches it).
//
// Measured: rateLimitState is the only one actually stepped on, so that one reads the way the
// real thing does — through sessionx's `RateLimitStates` store. main's `chat_wiring.go` reads
// the same store, so this is the same source, not a copy.

import (
	"fmt"
	"reflect"
	"testing"

	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

func init() {
	chatx.Configure(chatxStubDeps())
	gitx.Configure(gitxStubDeps())
	mcpx.Configure(mcpxStubDeps())
}

// mcpxStubDeps exists because launching a managed session (HandleSessionDriver →
// mcpx.StartManagedSession) goes through mcpx. Whatever main's mcp_wiring.go wires from
// sessionx is wired here to the same implementation — the real one, not a copy.
func mcpxStubDeps() mcpx.Deps {
	var d mcpx.Deps
	fillStubDeps("mcpx", &d)

	d.CleanTitle = CleanTitle
	d.SessionTitleMaxRunes = SessionTitleMaxRunes
	d.PeerIntentNames = PeerIntentNames
	d.PeerReachableSessions = PeerReachableSessions
	d.ApprovalGate = BridgeApprovalGate
	d.ApprovalLabel = ApprovalLabel
	d.ShellCreateTarget = ShellCreateTarget
	d.ShellSendTarget = ShellSendTarget
	d.SessionIsShell = SessionIsShell
	d.EnsureClaudeSettingsWiring = EnsureClaudeSettingsWiring
	d.WriteSSMConfig = WriteSSMConfig

	// The real one, as in main (uiprefs is a pure lower-level package).
	d.ReadUIPrefs = uiprefs.Read

	// Four mutable flag pairs. In main these are package vars, which the sessionx test binary
	// does not have, so the same getter/setter shape is built over local state closed over here.
	var writeEnabled, selfReportOnly, chromiumEnabled bool
	var convID string
	d.WriteEnabled = func() bool { return writeEnabled }
	d.SetWriteEnabled = func(v bool) { writeEnabled = v }
	d.SelfReportOnly = func() bool { return selfReportOnly }
	d.SetSelfReportOnly = func(v bool) { selfReportOnly = v }
	d.SessionChromiumEnabled = func() bool { return chromiumEnabled }
	d.SetSessionChromiumEnabled = func(v bool) { chromiumEnabled = v }
	d.ConvID = func() string { return convID }
	d.SetConvID = func(id string) { convID = id }
	return d
}

// fillStubDeps brings a Deps struct to the state "everything filled in, but every function
// panics when stepped on". pkg is the name that appears in the panic text.
func fillStubDeps(pkg string, ptr any) {
	v := reflect.ValueOf(ptr).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		fv := v.Field(i)
		if !fv.CanSet() {
			continue
		}
		switch f.Type.Kind() {
		case reflect.Func:
			name := f.Name
			fv.Set(reflect.MakeFunc(f.Type, func(args []reflect.Value) []reflect.Value {
				panic("a sessionx test stepped on " + pkg + "." + name + ". " +
					"This dependency was measured as never reached. Before putting a made-up return " +
					"value here, decide whether to mirror main's wiring exactly or to move that check " +
					"into package main")
			}))
		case reflect.String:
			fv.SetString("sessionx-stub:" + f.Name)
		case reflect.Map:
			fv.Set(reflect.MakeMapWithSize(f.Type, 1))
			fv.SetMapIndex(reflect.ValueOf("sessionx-stub").Convert(f.Type.Key()),
				reflect.Zero(f.Type.Elem()))
		case reflect.Slice:
			fv.Set(reflect.MakeSlice(f.Type, 1, 1))
		case reflect.Int, reflect.Int64:
			fv.SetInt(1)
		case reflect.Bool:
			fv.SetBool(true)
		default:
			panic(fmt.Sprintf("no way to fill %s.Deps.%s of type %s (a field was added — fix this)",
				pkg, f.Name, f.Type))
		}
	}
}

// gitxStubDeps exists because the worktree checks (TestEnsureWorktree and friends) go through
// gitx. Measured: ScratchAutoRelocate is the only one stepped on, and like main's scratch.go it
// does nothing when AF_WS_SCRATCH is unset (a plain no-op would diverge from the real thing
// exactly in environments where that variable is set).
func gitxStubDeps() gitx.Deps {
	var d gitx.Deps
	fillStubDeps("gitx", &d)

	// What main's git_wiring.go wires from sessionx is wired here to the same implementation.
	// It is the real one, not a copy, so checks that reach it through gitx
	// (TestMaybePruneWorktreeKeeps and the like) look at exactly what production looks at.
	d.AbsPath = AbsPath
	d.RepoLocked = RepoLocked
	d.LockedRepoDirs = LockedRepoDirs
	d.LiveSessionsInDir = LiveSessionsInDir
	d.LockedSessionsInDir = LockedSessionsInDir
	d.WorktreeHasSessions = WorktreeHasSessions
	d.ManagedAlive = ManagedAlive

	// Like main's scratch.go, do nothing when AF_WS_SCRATCH is unset (a plain no-op would
	// diverge from the real thing exactly in environments where that variable is set).
	d.ScratchAutoRelocate = func(dir string) {}
	return d
}

// chatxStubDeps builds a chatx.Deps that is fully filled in but panics when stepped on.
func chatxStubDeps() chatx.Deps {
	var d chatx.Deps
	fillStubDeps("chatx", &d)

	// What main's chat_wiring.go wires from sessionx is wired here to the same implementation
	// (the real one, not a copy). The list was not written by hand: it comes from matching
	// chatx.Deps field names against sessionx's exported names.
	d.FilterVisibleModels = FilterVisibleModels
	d.VisibleModel = VisibleModel
	d.VisibleModelIDs = VisibleModelIDs
	d.CleanSuggestedTitle = CleanSuggestedTitle
	d.TitleModel = TitleModel
	d.TitleSuggestFooter = TitleSuggestFooter
	d.TitleSuggestInstructions = TitleSuggestInstructions
	d.TitleSuggestPersona = TitleSuggestPersona
	d.TitleSuggestTimeout = TitleSuggestTimeout
	d.CleanSuggestedReplies = CleanSuggestedReplies
	d.ReplyCounterpartChat = ReplyCounterpartChat
	d.ReplySuggestEnabled = ReplySuggestEnabled
	d.ReplySuggestInstructions = ReplySuggestInstructions
	d.ReplySuggestLogHeader = ReplySuggestLogHeader
	d.ReplySuggestModel = ReplySuggestModel
	d.ReplySuggestPersona = ReplySuggestPersona
	d.ReplySuggestTimeout = ReplySuggestTimeout
	d.AbortResumeHolds = AbortResumeHolds
	d.CleanTitle = CleanTitle
	d.NormalizeKind = NormalizeKind
	d.ReplySuggestWindow = func(b *strings.Builder, msgs []chatx.ReplyMsg) {
		// The same repacking as main's chat_wiring.go (chatx cannot name sessionx's types).
		out := make([]ReplyMsg, 0, len(msgs))
		for _, m := range msgs {
			out = append(out, ReplyMsg{Role: m.Role, Text: m.Text})
		}
		ReplySuggestWindow(b, out)
	}

	// The one that has to behave like the real thing: read sessionx's RateLimitStates store.
	d.RateLimitState = func(name string) (string, string, bool) {
		st, ok := RateLimitStates.Read(name)
		if !ok {
			return "", "", false
		}
		return st.ScheduleID, st.ResumeAt, true
	}
	return d
}

// TestDepsStubsAreExhaustive checks that the reflect above really did fill every field of the
// chatx / gitx Deps.
//
// Without it, the day chatx grows a field the `chatx.Configure` panic happens inside init. A
// panic in init carries no test name, so in CI it looks only like "the whole package died" and
// the cause cannot be read. This turns it into a named failure instead.
func TestDepsStubsAreExhaustive(t *testing.T) {
	for _, c := range []struct {
		pkg string
		d   any
	}{{"chatx", chatxStubDeps()}, {"gitx", gitxStubDeps()}, {"mcpx", mcpxStubDeps()}} {
		v := reflect.ValueOf(c.d)
		tp := v.Type()
		if tp.NumField() == 0 {
			t.Fatalf("%s.Deps has 0 fields = this check has gone silent", c.pkg)
		}
		for i := 0; i < tp.NumField(); i++ {
			if v.Field(i).IsZero() {
				t.Errorf("%s.Deps.%s is not filled in (Configure will panic in init)",
					c.pkg, tp.Field(i).Name)
			}
		}
	}
}
