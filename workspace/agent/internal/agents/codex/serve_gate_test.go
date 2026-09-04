package codex

import (
	"errors"
	"testing"
)

// stubLoggedIn swaps the `codex login status` probe for the duration of a test.
func stubLoggedIn(t *testing.T, v bool) {
	t.Helper()
	prev := loggedIn
	loggedIn = func() bool { return v }
	t.Cleanup(func() { loggedIn = prev })
}

// gateSupervisor returns a SEPARATE supervisor pointed at a port nothing listens
// on, so Ensure takes the "would have to spawn a daemon" branch.
//
// The package-shared Serve() is deliberately not used: if another test in this package
// leaves itself attached to a fake app-server, Ensure here takes the "already up" shortcut
// and passes everything through, so the gate is never measured at all (this happened).
// Shutdown is registered as cleanup so that, if the gate breaks and a daemon really does
// start, the test does not leave 110 MB behind (which is what happened on the opencode side).
func gateSupervisor(t *testing.T) *Supervisor {
	t.Helper()
	t.Setenv(appServerAddrEnv, "ws://127.0.0.1:1")
	s := &Supervisor{}
	t.Cleanup(s.Shutdown)
	return s
}

// No app-server may be started in a logged-out workspace. The app-server itself will happily
// listen without checking auth, wasting a measured ~110 MB RSS for nothing.
func TestEnsureRefusesToSpawnWhenLoggedOut(t *testing.T) {
	s := gateSupervisor(t)
	stubLoggedIn(t, false)
	_, _, err := s.Ensure()
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("Ensure error = %v, want ErrNotLoggedIn", err)
	}
}

// Being disabled wins over everything: it must fail before the logged-out check, so that no
// `codex login status` exec runs in a workspace where the app-server is disabled.
func TestEnsureDisabledBeatsTheAuthGate(t *testing.T) {
	t.Setenv("AF_CODEX_APP_SERVER_DISABLE", "1")
	probed := false
	prev := loggedIn
	loggedIn = func() bool { probed = true; return true }
	t.Cleanup(func() { loggedIn = prev })
	if _, _, err := (&Supervisor{}).Ensure(); err == nil {
		t.Fatal("Ensure succeeded while the app-server is disabled")
	}
	if probed {
		t.Fatal("ran codex login status even though the app-server is disabled")
	}
}

// The demand count must always include the TUI route. Drop it and the moment managed hits 0
// we pull the backend (codex --remote) out from under live TUI sessions.
func TestDependentsCountsTUISessions(t *testing.T) {
	prev := TUIDependents
	t.Cleanup(func() { TUIDependents = prev })

	TUIDependents = func() int { return 0 }
	if got := dependents(); got != 0 {
		t.Fatalf("dependents = %d, want 0 on an empty workspace", got)
	}
	TUIDependents = func() int { return 2 }
	if got := dependents(); got != 2 {
		t.Fatalf("dependents = %d, want 2 (TUI sessions alone are demand)", got)
	}
}

// Managed handles are counted as registered, not as live: when the daemon dies runtimeLost
// clears alive on every handle, so counting live makes the very situation that needs
// recovery look like zero demand and inverts both the restart and the auto-stop decision.
func TestDependentsCountsRegisteredHandlesNotLiveOnes(t *testing.T) {
	prev := TUIDependents
	TUIDependents = func() int { return 0 }
	t.Cleanup(func() { TUIDependents = prev })

	handlesMu.Lock()
	handles["gate-test"] = &threadHandle{name: "gate-test"} // alive=false, i.e. the runtime was lost
	handlesMu.Unlock()
	t.Cleanup(func() {
		handlesMu.Lock()
		delete(handles, "gate-test")
		handlesMu.Unlock()
	})

	if got := len(liveHandles()); got != 0 {
		t.Fatalf("liveHandles = %d, want 0 (the premise of this test)", got)
	}
	if got := dependents(); got != 1 {
		t.Fatalf("dependents = %d, want 1 - a handle on a dead runtime is demand too", got)
	}
}

// Never fold up while there is demand: a re-check that closes the race between the watch
// loop's decision and the stop.
func TestStopIfIdleRefusesWhileNeeded(t *testing.T) {
	prev := TUIDependents
	TUIDependents = func() int { return 1 }
	t.Cleanup(func() { TUIDependents = prev })

	s := &Supervisor{up: true, watching: true}
	if s.stopIfIdle() {
		t.Fatal("stopped while there was still demand")
	}
	if !s.up {
		t.Fatal("cleared up even though the stop was skipped")
	}
}

// For a supervisor that is already down, report "stopped" and step off the watch.
func TestStopIfIdleOnAlreadyDownStopsWatching(t *testing.T) {
	prev := TUIDependents
	TUIDependents = func() int { return 0 }
	t.Cleanup(func() { TUIDependents = prev })

	s := &Supervisor{watching: true}
	if !s.stopIfIdle() {
		t.Fatal("did not step off the watch for a supervisor that is down")
	}
	if s.watching {
		t.Fatal("watching left set - the next Ensure cannot re-establish the watch")
	}
}
