package opencode

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// unspawnableAddr points the supervisor at an address that (a) can never be
// healthy, so ensure reaches the "would have to spawn" branch, and (b) fails
// splitServeAddr, which is the very next step — so a test that gets PAST the gate
// still cannot start a real daemon.
//
// "A port nobody uses" was not enough: this container has ip_unprivileged_port_start=0, so even
// :1 can really be bound, and a test that got past the gate left a 310 MB serve running (real
// damage). The package-shared Serve() is avoided too: if another test in this package leaves the
// daemon up, ensure takes the shortcut past the gate and the check measures nothing while
// looking green (which is what happened on the codex side).
func unspawnableAddr(t *testing.T) *Supervisor {
	t.Helper()
	t.Setenv(serveAddrEnv, "tcp://127.0.0.1:7799")
	// HOME is isolated too. ensure goes through secrets.Load() at the connection gate, and
	// without isolation it decides against the user's real credential store under
	// ~/.config/agent-fleet (creating `secrets.enc.lock` there) — on a dev machine already
	// connected to opencode, the "do not spawn while disconnected" check then proves nothing.
	// Measured: only the tests using this helper left a lock file in the real HOME.
	t.Setenv("HOME", t.TempDir())
	s := &Supervisor{}
	t.Cleanup(s.Shutdown)
	return s
}

// stubUsagePref forces the usage-route setting for the duration of a test.
func stubUsagePref(t *testing.T, v string) {
	t.Helper()
	prev := UsagePref
	UsagePref = func() string { return v }
	t.Cleanup(func() { UsagePref = prev })
}

// serve must not be spawned in a disconnected workspace (the default, UsageOff). serve can
// listen without looking at auth at all, and a measured ~305 MB RSS would be wasted outright.
func TestEnsureRefusesToSpawnWhenNotConnected(t *testing.T) {
	s := unspawnableAddr(t)
	stubUsagePref(t, UsageOff)
	_, _, err := s.Ensure()
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Ensure error = %v, want ErrNotConnected", err)
	}
}

// The OAuth device flow alone may spawn while disconnected: this is the very API set that turns
// "disconnected" into "connected", so refusing on those grounds means never being able to log in
// (chicken and egg). Only getting past the gate is checked (the address can never reach spawn).
func TestEnsureAllowsUnauthedForTheOAuthFlow(t *testing.T) {
	s := unspawnableAddr(t)
	stubUsagePref(t, UsageOff)
	_, _, err := s.ensure(true)
	if errors.Is(err, ErrNotConnected) {
		t.Fatal("the OAuth flow was stopped by the disconnected gate — nobody could ever log in")
	}
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("ensure error = %v, want the address parse error (proof it got past the gate)", err)
	}
}

// Disabling wins outright: it must fail before the connection check.
func TestEnsureDisabledBeatsTheConnectionGate(t *testing.T) {
	t.Setenv("AF_OPENCODE_SERVE_DISABLE", "1")
	stubUsagePref(t, UsageFree)
	if _, _, err := (&Supervisor{}).Ensure(); err == nil {
		t.Fatal("Ensure succeeded while serve is disabled")
	}
}

// A device flow in progress must count as demand. Without it the daemon is folded up under the
// user while they are still approving in the browser.
func TestDependentsHoldsDuringTheOAuthFlow(t *testing.T) {
	prevAt := oauthTouchAt
	t.Cleanup(func() {
		oauthTouchMu.Lock()
		oauthTouchAt = prevAt
		oauthTouchMu.Unlock()
	})

	oauthTouchMu.Lock()
	oauthTouchAt = time.Time{}
	oauthTouchMu.Unlock()
	if got := dependents(); got != 0 {
		t.Fatalf("dependents = %d, want 0 on an idle workspace", got)
	}

	oauthTouch()
	if got := dependents(); got != 1 {
		t.Fatalf("dependents = %d, want 1 while an OAuth flow is in progress", got)
	}

	// Past the window the demand is gone (an abandoned flow must not keep the daemon alive
	// forever).
	oauthTouchMu.Lock()
	oauthTouchAt = time.Now().Add(-oauthHoldTTL - time.Second)
	oauthTouchMu.Unlock()
	if got := dependents(); got != 0 {
		t.Fatalf("dependents = %d, want 0 once the OAuth window has expired", got)
	}
}

// Do not fold up while demand exists (the re-check that closes the race between the watch
// loop's decision and the stop).
func TestStopIfIdleRefusesWhileNeeded(t *testing.T) {
	oauthTouch()
	t.Cleanup(func() {
		oauthTouchMu.Lock()
		oauthTouchAt = time.Time{}
		oauthTouchMu.Unlock()
	})
	s := &Supervisor{up: true, watching: true}
	if s.stopIfIdle() {
		t.Fatal("stopped while demand still existed")
	}
	if !s.up {
		t.Fatal("up was cleared even though the stop was skipped")
	}
}
