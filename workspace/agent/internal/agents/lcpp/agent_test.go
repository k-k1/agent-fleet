package lcpp

import (
	"errors"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// TestAgentKindAndCaps pins the facts the server-side gates (sessionx's create default, the
// /driver switch, and scripts/docs-check.py's exact-match check against ref/agents.md) rely
// on: the kind string, and exactly which Caps flags are true now that the store (store.go)
// and the managed driver (driver.go) actually back them. Never flip one of these on without
// the guide table and docs-check agreeing (see this package's driver.go/agent.go doc
// comments for what each flag is actually backed by).
func TestAgentKindAndCaps(t *testing.T) {
	a := New()
	if got := a.Kind(); got != session.KindLcpp {
		t.Fatalf("Kind() = %q, want %q", got, session.KindLcpp)
	}
	got := a.Caps()
	want := agents.Caps{
		ManagedOnly:      true,
		CanTranscript:    true,
		CanFork:          true,
		CanForkAt:        true,
		PermissionChoice: true,
	}
	if got != want {
		t.Fatalf("Caps() = %+v, want %+v", got, want)
	}
}

// TestBuildLaunchAlwaysErrors pins ADR 0093 decision 2: lcpp has no tmux pane program at all,
// so BuildLaunch must refuse unconditionally regardless of the passed meta.
func TestBuildLaunchAlwaysErrors(t *testing.T) {
	a := New()
	_, err := a.BuildLaunch(session.Meta{Name: "x", Dir: "/tmp"}, agents.LaunchOpts{})
	if !errors.Is(err, ErrNoTerminalRoute) {
		t.Fatalf("BuildLaunch err = %v, want ErrNoTerminalRoute", err)
	}
}

// TestWireLiveNotAliveIsZeroValue pins WireLive's alive=false shape: no live handle to read
// LastSay from, and Resumable defaults true (there is no "working dir gone" concept for a
// managed-only kind the way claude/opencode's tui route has).
func TestWireLiveNotAliveIsZeroValue(t *testing.T) {
	testHome(t)
	a := New()
	li := a.WireLive(session.Meta{Name: "unknown-session"}, false)
	if li.State != "" || !li.Resumable || li.LastSay != "" {
		t.Fatalf("WireLive(alive=false) = %+v, want an empty/resumable zero value", li)
	}
	a.ClearResume("anything") // must not panic
}

// TestWireLiveContextRecordedWindow pins ADR 0093 decision 8's WireLive half: once a turn has
// completed, Context comes back with WindowSource="recorded" (never a guess), sourced from
// the store's own last usage record — and it does so whether or not the session is alive
// (claude's own WireLive reads Context unconditionally too, for the same "a stopped card
// still shows its last known fill" reason).
func TestWireLiveContextRecordedWindow(t *testing.T) {
	testHome(t)
	m := session.Meta{Name: "ctx-sess", Dir: t.TempDir(), Model: "qwen3-30b"}
	s := Open(sidFor(m))
	if _, err := s.AppendUsage(harness.Usage{PromptTokens: 111, CompletionTokens: 22}, 8192); err != nil {
		t.Fatalf("AppendUsage: %v", err)
	}
	a := New()
	for _, alive := range []bool{true, false} {
		li := a.WireLive(m, alive)
		if li.Context == nil {
			t.Fatalf("alive=%v: Context = nil, want the recorded usage", alive)
		}
		if li.Context.Fresh != 111 || li.Context.Model != "qwen3-30b" {
			t.Fatalf("alive=%v: Context = %+v, want Fresh=111 Model=qwen3-30b", alive, li.Context)
		}
		if li.Context.Window != 8192 || li.Context.WindowSource != "recorded" {
			t.Fatalf("alive=%v: Context window = %d/%q, want 8192/recorded (never a WindowGuess)",
				alive, li.Context.Window, li.Context.WindowSource)
		}
	}
}

// TestWireLiveContextUnresolvedWindowOmitsSource is the negative control: a turn whose window
// could not be resolved must not be reported as "recorded" — that would misrepresent a number
// nobody actually measured as exact.
func TestWireLiveContextUnresolvedWindowOmitsSource(t *testing.T) {
	testHome(t)
	m := session.Meta{Name: "ctx-sess-no-window", Dir: t.TempDir()}
	s := Open(sidFor(m))
	if _, err := s.AppendUsage(harness.Usage{PromptTokens: 50, CompletionTokens: 5}, 0); err != nil {
		t.Fatalf("AppendUsage: %v", err)
	}
	li := New().WireLive(m, false)
	if li.Context == nil {
		t.Fatal("Context = nil, want the recorded token usage even without a window")
	}
	if li.Context.Window != 0 || li.Context.WindowSource != "" {
		t.Fatalf("Context window = %d/%q, want 0/\"\" (unresolved must not claim recorded)",
			li.Context.Window, li.Context.WindowSource)
	}
}

// TestTranscriptEmptyStoreIsOkTrue pins Transcript()'s "no conversation yet" shape: unlike the
// old stub (Caps().CanTranscript was false, ok was always false), an lcpp session with no
// turns sent yet reads back ok=true with an empty turn list — the store is genuinely there,
// it is just empty, and CanTranscript is true (TestAgentKindAndCaps).
func TestTranscriptEmptyStoreIsOkTrue(t *testing.T) {
	testHome(t)
	a := New()
	td, ok := a.Transcript(session.Meta{Name: "never-sent", Dir: t.TempDir()})
	if !ok {
		t.Fatal("Transcript ok = false, want true (CanTranscript is now true)")
	}
	if len(td.Turns) != 0 || td.Pending != nil || td.Queued != nil {
		t.Fatalf("Transcript = %+v, want an empty conversation", td)
	}
}

// TestTranscriptFeedsTheChangedFilesAggregation walks the whole chain a changed-files row is
// made of, in one test: a recorded `edit` tool call → the transcript part (fileedits.go) →
// what sessionx folds (transcript.FileEditsInTurn). The middle step is where this kind used
// to stop, and the last step is the one that fails QUIETLY — absEditPath drops a relative
// path when Turn.Cwd is empty, so a parser that works and a Cwd that is missing produce the
// same empty strip as no parser at all.
func TestTranscriptFeedsTheChangedFilesAggregation(t *testing.T) {
	testHome(t)
	m := session.Meta{Name: "files-sess", Dir: t.TempDir()}
	s := Open(sidFor(m))
	if _, err := s.AppendUser("fix it"); err != nil {
		t.Fatalf("AppendUser: %v", err)
	}
	if _, err := s.AppendMessage(harness.Message{
		Role: harness.RoleAssistant, Content: "editing",
		ToolCalls: []harness.ToolCall{
			{ID: "c1", Name: "edit", Arguments: `{"path":"a.go","old_string":"old\n","new_string":"new\n"}`},
			{ID: "c2", Name: "bash", Arguments: `{"command":"go build ./..."}`},
		},
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	td, ok := New().Transcript(m)
	if !ok {
		t.Fatal("Transcript ok = false")
	}
	for _, tn := range td.Turns {
		if tn.Cwd != m.CWD() {
			t.Fatalf("turn %d Cwd = %q, want %q — a relative edit path cannot be anchored without it",
				tn.Idx, tn.Cwd, m.CWD())
		}
	}

	var edits []transcript.FileEdit
	for _, tn := range td.Turns {
		edits = append(edits, transcript.FileEditsInTurn(tn)...)
	}
	// One record, not two: the bash call in the same turn is the negative control.
	if len(edits) != 1 {
		t.Fatalf("FileEditsInTurn over the session = %+v, want exactly the one edit call", edits)
	}
	got := edits[0]
	if got.Path != "a.go" || got.Cwd != m.CWD() || got.Verb != "edit" || got.Added != 1 || got.Removed != 1 {
		t.Fatalf("folded edit = %+v, want a.go under %q as edit +1 -1", got, m.CWD())
	}
}
