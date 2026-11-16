package main

import (
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// Every kind that declares fixed routes resolves which one it runs on at launch (ADR 0138 D2,
// D11): without a resolver its sessions would be recorded on nothing.
func TestEveryKindWithFixedRoutesResolves(t *testing.T) {
	for _, k := range sessionx.RegisteredKinds() {
		a, _ := sessionx.RegisteredAgent(k)
		_, resolves := a.(agents.CredentialResolver)
		if fixed := agents.FixedRoutesOf(a); len(fixed) > 0 != resolves {
			t.Errorf("kind %q: fixed routes %v, CredentialResolver %v; a kind with fixed routes resolves among them", k, fixed, resolves)
		}
	}
}

// stubResolver answers UsableRoutes after a delay.
type stubResolver struct {
	agents.Agent
	usable []string
	delay  time.Duration
}

func (stubResolver) ResolveLaunch(session.Meta) routes.Launch { return routes.Launch{} }
func (s stubResolver) UsableRoutes() []string {
	time.Sleep(s.delay)
	return s.usable
}

// The live kind catalog reports each kind's usable routes; "none" is an empty list, and a kind
// that does not answer within the budget is left out (unknown) instead of holding the answer up.
func TestKindCatalogUsableRoutes(t *testing.T) {
	// The catalog reads the held policy; the Agent's own store, opened on first use, takes the
	// start environment and the tmux server with it, which in a live Workspace has taken up to 16 s
	// (measured) and broke the budget below.
	useHidingPolicy(t, routepolicy.Policy{Revision: 1})
	old := usableRoutesBudget
	usableRoutesBudget = 300 * time.Millisecond
	t.Cleanup(func() { usableRoutesBudget = old })
	for k, s := range map[string]stubResolver{
		session.KindCodex: {usable: []string{routes.RouteOpenAIAPI}},
		session.KindMuse:  {usable: nil},
		session.KindAgy:   {usable: []string{routes.RouteAgyLogin}, delay: 2 * time.Second},
	} {
		s.Agent = agentreg.Of(k)
		t.Cleanup(agentreg.SwapForTest(k, s))
	}
	got := map[string]*[]string{}
	start := time.Now()
	for _, e := range buildKindCatalog(true).Kinds {
		got[e.Kind] = e.UsableRoutes
	}
	if el := time.Since(start); el > 1500*time.Millisecond {
		t.Errorf("the catalog took %v; a slow kind must not hold it past the budget", el)
	}
	if u := got[session.KindCodex]; u == nil || !slices.Equal(*u, []string{routes.RouteOpenAIAPI}) {
		t.Errorf("codex usable = %v", u)
	}
	if u := got[session.KindMuse]; u == nil || len(*u) != 0 {
		t.Errorf("muse usable = %v, want an empty list (none)", u)
	}
	if u := got[session.KindAgy]; u != nil {
		t.Errorf("agy usable = %v, want absent (did not answer in time)", *u)
	}
	if u := got[session.KindShell]; u != nil {
		t.Errorf("shell usable = %v, want absent (no resolver)", *u)
	}
}

// writerResolver answers at once but keeps probing: it writes under HOME after delay, the way a
// slow CLI probe does after the catalog has stopped waiting for it.
type writerResolver struct {
	agents.Agent
	delay    time.Duration
	finished *atomic.Bool
}

func (writerResolver) ResolveLaunch(session.Meta) routes.Launch { return routes.Launch{} }
func (w writerResolver) UsableRoutes() []string {
	time.Sleep(w.delay)
	_ = os.WriteFile(filepath.Join(os.Getenv("HOME"), "probe-cache"), []byte("x"), 0o600)
	w.finished.Store(true)
	return nil
}

// A test that builds the live catalog does not end while a probe the catalog gave up on is still
// running: that probe would write into the test's temporary HOME during its removal.
func TestKindCatalogProbesAreSettledBeforeTheTestEnds(t *testing.T) {
	var finished atomic.Bool
	t.Run("catalog", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		useHidingPolicy(t, routepolicy.Policy{Revision: 1})
		old := usableRoutesBudget
		usableRoutesBudget = 20 * time.Millisecond
		t.Cleanup(func() { usableRoutesBudget = old })
		w := writerResolver{Agent: agentreg.Of(session.KindAgy), delay: 400 * time.Millisecond, finished: &finished}
		t.Cleanup(agentreg.SwapForTest(session.KindAgy, w))
		buildKindCatalog(true)
		if finished.Load() {
			t.Fatal("fixture: the probe finished inside the budget")
		}
	})
	if !finished.Load() {
		t.Error("the test ended while a catalog probe was still running")
	}
}
