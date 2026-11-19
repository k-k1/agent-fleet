package opencode

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// fakeBusyServe is a healthy daemon with one Terminal turn running; it records the aborts.
func fakeBusyServe(t *testing.T) (aborted func() string) {
	t.Helper()
	var mu sync.Mutex
	got := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/info":
			_, _ = w.Write([]byte(`{"version":"2.0.28"}`))
		case r.URL.Path == "/api/session/active":
			_, _ = w.Write([]byte(`{"data":{"ses_busy":{"type":"running"}}}`))
		case strings.HasSuffix(r.URL.Path, "/interrupt"):
			mu.Lock()
			got = r.URL.Path
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(serveAddrEnv, srv.URL)
	m := session.Meta{Name: "oc-busy", Dir: t.TempDir(), Kind: session.KindOpencode, Driver: session.DriverTUI}
	sid := session.UUID(m.Dir, m.Name)
	markSlotMode(sid, modeShared)
	sids.Write(sid, "ses_busy")
	t.Cleanup(func() { slotModes.Remove(sid); sids.Remove(sid) })
	stubTerminalSlots(t, func() []session.Meta { return []session.Meta{m} })
	return func() string { mu.Lock(); defer mu.Unlock(); return got }
}

// With no restriction nothing in the policy's restart path acts: an adopted daemon, or one of
// another generation, is used as before, and a policy change aborts nothing.
func TestUnrestrictedServeIsNeverTouched(t *testing.T) {
	isolatedHome(t)
	aborted := fakeBusyServe(t)
	useIsolationPolicy(t, routepolicy.Policy{Revision: 1, Allowed: []string{}})
	s := &Supervisor{up: true, credGen: "something-else"} // adopted-looking, stale-looking
	if err := s.policyAdmitLocked(serveAddr()); err != nil {
		t.Errorf("an unrestricted Ensure was refused: %v", err)
	}
	s.onPolicyChange(routepolicy.Policy{Revision: 1, Allowed: []string{}})
	time.Sleep(200 * time.Millisecond)
	if a := aborted(); a != "" {
		t.Errorf("an unrestricted policy change aborted a turn: %s", a)
	}
}

// Under a restriction a daemon this Agent did not start is never used, while with no restriction
// it is adopted as before.
func TestAdoptedServeIsNotUsedUnderARestriction(t *testing.T) {
	isolatedHome(t)
	fakeBusyServe(t)
	useIsolationPolicy(t, routepolicy.Policy{Revision: 1, Allowed: []string{}})
	s := &Supervisor{}
	if _, _, err := s.Ensure(); err != nil {
		t.Fatalf("control: an unrestricted Ensure did not adopt the running daemon: %v", err)
	}
	useIsolationPolicy(t, anthropicOnly)
	if _, _, err := s.Ensure(); !errors.Is(err, ErrServeUnverified) {
		t.Errorf("a restricted Ensure reused an adopted daemon: %v", err)
	}
	if _, _, err := (&Supervisor{}).Ensure(); !errors.Is(err, ErrServeUnverified) {
		t.Errorf("a restricted Ensure adopted a daemon it did not start: %v", err)
	}
}

// A tighter policy replaces the daemon at once: a running turn is aborted without waiting for the
// ordinary drain (60 s here), and Ensure admits nothing while it is replaced or while a stale
// generation is in place.
func TestTighterPolicyRestartsTheServeWithoutADrain(t *testing.T) {
	isolatedHome(t)
	t.Setenv("AF_SECRET_KEY", "")
	t.Setenv("AF_OPENCODE_DRAIN_TIMEOUT_SEC", "60")
	aborted := fakeBusyServe(t)
	useIsolationPolicy(t, anthropicOnly)

	s := &Supervisor{up: true, cmd: nil, credGen: "stale"}
	start := time.Now()
	s.onPolicyChange(anthropicOnly)
	if !strings.Contains(aborted(), "ses_busy") {
		t.Fatalf("the running turn was not aborted (abort %q)", aborted())
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("the security restart waited %v, as a drain would", d)
	}
	// The adopted daemon (no process handle) path must not leave the restart flags set.
	s.mu.Lock()
	if s.securing || s.stopping {
		t.Errorf("flags left set after an adopted-daemon restart: securing=%v stopping=%v", s.securing, s.stopping)
	}
	s.mu.Unlock()

	// A daemon of the current generation, owned by this supervisor, while a replacement runs.
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	replacing := &Supervisor{up: true, cmd: cmd, credGen: credentialGen(), securing: true}
	if err := replacing.policyAdmitLocked(serveAddr()); !errors.Is(err, ErrServeUnverified) {
		t.Errorf("Ensure admitted a turn while the daemon was being replaced: %v", err)
	}
}

// Under a restriction, a daemon this supervisor started is used while its generation is the
// policy's current one; a stale one is refused and replaced: its process is stopped.
func TestStaleGenerationIsReplacedUnderARestriction(t *testing.T) {
	isolatedHome(t)
	t.Setenv("AF_SECRET_KEY", "")
	fakeBusyServe(t)
	useIsolationPolicy(t, anthropicOnly)
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	current := &Supervisor{up: true, cmd: cmd, credGen: credentialGen()}
	if err := current.policyAdmitLocked(serveAddr()); err != nil {
		t.Fatalf("a daemon of the current generation was refused: %v", err)
	}
	stale := &Supervisor{up: true, cmd: cmd, credGen: "stale"}
	stale.mu.Lock()
	err := stale.policyAdmitLocked(serveAddr())
	stale.mu.Unlock()
	if !errors.Is(err, ErrServeUnverified) {
		t.Fatalf("a stale generation was admitted: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		stale.mu.Lock()
		gone := !stale.up && stale.cmd == nil
		stale.mu.Unlock()
		if gone {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("the stale daemon was not stopped")
}

// The reconcile a security restart starts must not be refused by the restart's own securing
// flag: Ensure refuses while securing is set, and a managed session whose reconcile was refused
// stays stopped until its next resume. startReconcile is stubbed to run synchronously, so the
// supervisor is inspected at the exact instant the reconcile would start, before SecurityRestart
// returns.
func TestSecurityRestartClearsSecuringBeforeTheReconcileStarts(t *testing.T) {
	isolatedHome(t)
	t.Setenv("AF_SECRET_KEY", "")
	dead := httptest.NewServer(http.NotFoundHandler())
	t.Setenv(serveAddrEnv, dead.URL)
	dead.Close() // nothing listens: stopProcess does not wait, healthy() is false
	useIsolationPolicy(t, anthropicOnly)
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	s := &Supervisor{up: true, cmd: cmd, credGen: "stale"}
	started := false
	prev := startReconcile
	startReconcile = func(string) {
		started = true
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.securing || s.stopping || s.up || s.cmd != nil {
			t.Errorf("at reconcile start: securing=%v stopping=%v up=%v cmd=%v", s.securing, s.stopping, s.up, s.cmd)
		}
		if err := s.policyAdmitLocked(serveAddr()); err != nil {
			t.Errorf("the follow-up reconcile's Ensure was refused: %v", err)
		}
	}
	t.Cleanup(func() { startReconcile = prev })

	if !s.SecurityRestart("test") {
		t.Fatal("SecurityRestart stopped no process")
	}
	if !started {
		t.Fatal("no reconcile was started")
	}
}
