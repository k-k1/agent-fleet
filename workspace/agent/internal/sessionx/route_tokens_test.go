package sessionx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// routeTokenEnds records every name the lifecycle exits ended route tokens for (the test
// deps wire EndRouteTokens to recordRouteTokenEnd).
var routeTokenEnds struct {
	mu    sync.Mutex
	names []string
}

func recordRouteTokenEnd(name string) {
	routeTokenEnds.mu.Lock()
	routeTokenEnds.names = append(routeTokenEnds.names, name)
	routeTokenEnds.mu.Unlock()
}

// watchRouteTokenEnds starts recording afresh and returns a check that name's tokens were ended.
func watchRouteTokenEnds(t *testing.T) func(name, exit string) {
	t.Helper()
	routeTokenEnds.mu.Lock()
	routeTokenEnds.names = nil
	routeTokenEnds.mu.Unlock()
	return func(name, exit string) {
		t.Helper()
		routeTokenEnds.mu.Lock()
		defer routeTokenEnds.mu.Unlock()
		if !slices.Contains(routeTokenEnds.names, name) {
			t.Fatalf("%s did not end the route tokens of %q (ended: %v)", exit, name, routeTokenEnds.names)
		}
	}
}

// Every exit that ends a session's runtime ends its route tokens at once, through its real
// handler. Delete is ForgetRuntime here (trashSession lives in package main and runs HaltSession,
// covered by the halt case, and then ForgetRuntime).
func TestLifecycleExitsEndRouteTokens(t *testing.T) {
	t.Run("halt of a running session", func(t *testing.T) {
		const name = "rtkhalt1"
		fakeTmuxOnlyFor(t, session.TmuxName(name))
		session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})
		ended := watchRouteTokenEnds(t)
		if w := callHandler(HandleHaltSession, http.MethodPost, name, ""); w.Code != http.StatusOK {
			t.Fatalf("halt = %d %s", w.Code, w.Body)
		}
		ended(name, "halt")
	})
	t.Run("halt of a session whose pane already ended", func(t *testing.T) {
		home := writebackEnv(t)
		const name = "rtkhalt2"
		session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindClaude})
		ended := watchRouteTokenEnds(t)
		if w := callHandler(HandleHaltSession, http.MethodPost, name, ""); w.Code != http.StatusOK {
			t.Fatalf("halt = %d %s", w.Code, w.Body)
		}
		ended(name, "halt of a stopped session")
	})
	t.Run("archive", func(t *testing.T) {
		const name = "rtkarc1"
		fakeTmuxOnlyFor(t, session.TmuxName(name))
		session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})
		ended := watchRouteTokenEnds(t)
		if w := callHandler(HandleArchiveSession, http.MethodPost, name, ""); w.Code != http.StatusOK {
			t.Fatalf("archive = %d %s", w.Code, w.Body)
		}
		ended(name, "archive")
	})
	t.Run("recreate", func(t *testing.T) {
		home := writebackEnv(t)
		const name = "rtkrec1"
		session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindClaude})
		orig := launchTmuxFn
		launchTmuxFn = func(session.Meta, bool) error { return http.ErrAbortHandler }
		t.Cleanup(func() { launchTmuxFn = orig })
		ended := watchRouteTokenEnds(t)
		callHandler(HandleRecreateSession, http.MethodPost, name, "")
		ended(name, "recreate")
	})
	t.Run("delete", func(t *testing.T) {
		home := writebackEnv(t)
		const name = "rtkdel1"
		m := session.Meta{Name: name, Dir: home, Kind: session.KindClaude}
		session.WriteMeta(m)
		ended := watchRouteTokenEnds(t)
		ForgetRuntime(m)
		ended(name, "delete")
	})
	t.Run("delete of an orphan pane", func(t *testing.T) {
		const name = "rtkorph1"
		fakeTmuxOnlyFor(t, session.TmuxName(name))
		prev := deps.RemoveTerminalHistory
		deps.RemoveTerminalHistory = func(string) {}
		t.Cleanup(func() { deps.RemoveTerminalHistory = prev })
		ended := watchRouteTokenEnds(t)
		if w := callHandler(HandleStopSession, http.MethodPost, name, ""); w.Code != http.StatusOK {
			t.Fatalf("stop = %d %s", w.Code, w.Body)
		}
		ended(name, "an orphan's kill")
	})
	t.Run("the list finds the pane gone", func(t *testing.T) {
		home := writebackEnv(t)
		const name = "rtkgone1"
		session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindClaude, CreatedAt: time.Now().Format(time.RFC3339)})
		prev := deps.FinalizeSessionUsage
		deps.FinalizeSessionUsage = func(session.Meta) {}
		t.Cleanup(func() { deps.FinalizeSessionUsage = prev })
		ended := watchRouteTokenEnds(t)
		rec := httptest.NewRecorder()
		HandleListSessions(rec, httptest.NewRequest(http.MethodGet, "/sessions", nil))
		if m, _ := session.ReadMeta(name); m.StoppedAt == "" {
			t.Fatal("the list did not stamp the stop; the setup is wrong")
		}
		ended(name, "the list's death detection")
	})
	t.Run("a move's orphan target", func(t *testing.T) {
		fakeTmux(t)
		ended := watchRouteTokenEnds(t)
		if err := stopOrphanTarget(moveRecord{Target: "rtkmov1"}); err != nil {
			t.Fatal(err)
		}
		ended("rtkmov1", "a move's orphan cleanup")
	})
	t.Run("a driver switch", func(t *testing.T) {
		fakeTmux(t)
		t.Setenv("AF_CODEX_APP_SERVER_DISABLE", "1")
		const name = "rtkdrv1"
		session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex})
		ended := watchRouteTokenEnds(t)
		postDriver(t, name, `{"driver":"managed"}`) // the managed start fails; the old pane is gone by then
		ended(name, "a driver switch")
	})
}

// The selection changes at the route switch's store and back at its rollback; the tokens of the
// selection being left end there, before the next launch mints its own. The halt is a stand-in
// in these tests, so what is seen is the store's and the rollback's own call.
func TestRouteSwitchEndsRouteTokensWhereTheSelectionChanges(t *testing.T) {
	t.Run("store", func(t *testing.T) {
		e := newSwitchEnv(t, subscriptionRoute)
		e.alive, e.state = false, "stopped"
		recordSwitchTokenEnds(t, e)
		if _, err := e.run(); err != nil {
			t.Fatal(err)
		}
		end, launch := indexOf(e.events, "end-tokens"), indexOf(e.events, "launch:anthropic-api")
		if end < 0 || launch < 0 || end > launch {
			t.Fatalf("events = %v, want the tokens ended before the new route launches", e.events)
		}
	})
	t.Run("rollback", func(t *testing.T) {
		e := newSwitchEnv(t, subscriptionRoute)
		e.alive, e.state = false, "stopped"
		e.launchErr = []error{errors.New("tmux: no server")}
		recordSwitchTokenEnds(t, e)
		_, _ = e.run()
		failed := indexOf(e.events, "launch:anthropic-api")
		if failed < 0 || indexOf(e.events[failed+1:], "end-tokens") < 0 {
			t.Fatalf("events = %v, want the tokens ended again after the new route failed", e.events)
		}
	})
}

func recordSwitchTokenEnds(t *testing.T, e *switchEnv) {
	prev := deps.EndRouteTokens
	deps.EndRouteTokens = func(name string) {
		if name == e.name {
			e.events = append(e.events, "end-tokens")
		}
	}
	t.Cleanup(func() { deps.EndRouteTokens = prev })
}
