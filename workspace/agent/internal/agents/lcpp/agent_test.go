package lcpp

import (
	"errors"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
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
	a := New()
	li := a.WireLive(session.Meta{Name: "unknown-session"}, false)
	if li.State != "" || !li.Resumable || li.LastSay != "" {
		t.Fatalf("WireLive(alive=false) = %+v, want an empty/resumable zero value", li)
	}
	a.ClearResume("anything") // must not panic
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
