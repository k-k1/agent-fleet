package lcpp

import (
	"errors"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// TestAgentKindAndManagedOnlyCap pins the two facts the server-side gates (sessionx's create
// default and the /driver switch) rely on: the kind string, and Caps().ManagedOnly being the
// ONLY true flag at this stage (ADR 0093 stage 2 step 1 — no transcript store, no approval
// loop, no fork behind the others yet).
func TestAgentKindAndManagedOnlyCap(t *testing.T) {
	a := New()
	if got := a.Kind(); got != session.KindLcpp {
		t.Fatalf("Kind() = %q, want %q", got, session.KindLcpp)
	}
	got := a.Caps()
	want := agents.Caps{ManagedOnly: true}
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

// TestWireLiveAndTranscriptAreStillStubs guards against silently flipping a cap on before the
// work that would make it true (the managed driver / transcript store) actually lands — an
// easy mistake since Caps().ManagedOnly alone doesn't gate these.
func TestWireLiveAndTranscriptAreStillStubs(t *testing.T) {
	a := New()
	li := a.WireLive(session.Meta{}, true)
	if li.State != "" || li.Resumable || li.Context != nil || li.TokenSpends != nil {
		t.Fatalf("WireLive = %+v, want zero value", li)
	}
	if _, ok := a.Transcript(session.Meta{}); ok {
		t.Fatalf("Transcript ok = true, want false (no transcript store yet)")
	}
	a.ClearResume("anything") // must not panic
}
