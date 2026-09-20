package sessionx

// Regression tests for the two server-side gates ADR 0093 decision 2 asks for on a
// Caps.ManagedOnly kind (lcpp is the first): POST /sessions defaults an unspecified driver to
// managed instead of tui, and POST /sessions/{name}/driver refuses a switch TO tui. Both are
// paired with a negative control on an unaffected kind (shell / codex) to prove the new branch
// doesn't fire for kinds that still have a Terminal(CLI) route.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// TestCreateSessionDefaultsManagedOnlyKindToManaged pins the create-time default: an lcpp
// create with no driver field must be treated as "managed". Now that the managed driver is
// wired up (internal/agents/lcpp/driver.go), the create actually succeeds — Resume needs no
// engine at all for a brand new, empty conversation (its own settle() reads back zero
// records and lands on TurnCompleted with no network call), so this is a true positive
// control for the default flipping, not just the absence of a "fell through to tui" failure.
//
// Real tmux (isolateAgentState), not fakeTmux's stub: this create reaches allocSessionName,
// whose collision-retry loop never terminates against fakeTmux's has-session stub (it always
// answers "exists") — see the longer note on the negative control below.
func TestCreateSessionDefaultsManagedOnlyKindToManaged(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	isolateAgentState(t)
	dir := t.TempDir()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", HandleCreateSession)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var created session.Session
	do(t, srv, "POST", "/sessions", map[string]any{"dir": dir, "kind": "lcpp"}, http.StatusCreated, &created)
	m, ok := session.ReadMeta(created.Name)
	if !ok {
		t.Fatal("meta not persisted")
	}
	if m.DriverKind() != session.DriverManaged {
		t.Fatalf("driver = %q, want managed (the ManagedOnly default)", m.Driver)
	}
}

// TestCreateSessionLeavesTerminalRouteKindDefaultingToTUI is the negative control: a kind that
// still has a Terminal(CLI) route (shell) must keep defaulting an unspecified driver to tui and
// launch normally, unaffected by the new ManagedOnly branch.
//
// This needs a REAL tmux (isolateAgentState), not fakeTmux's stub: fakeTmux's has-session
// always answers "exists" (exit 0), which is fine for the driver-switch tests below (an
// already-named session) but sends allocSessionName's collision-retry loop into an infinite
// loop for a fresh create, which has to try candidate names until one comes back free.
func TestCreateSessionLeavesTerminalRouteKindDefaultingToTUI(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	isolateAgentState(t)
	dir := t.TempDir()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", HandleCreateSession)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var created session.Session
	do(t, srv, "POST", "/sessions", map[string]any{"dir": dir, "kind": "shell"}, http.StatusCreated, &created)
	m, ok := session.ReadMeta(created.Name)
	if !ok {
		t.Fatal("meta not persisted")
	}
	if m.DriverKind() != session.DriverTUI {
		t.Fatalf("driver = %q, want tui default left untouched", m.Driver)
	}
}

// TestHandleSessionDriverRejectsTUITargetForManagedOnlyKind pins the /driver switch's
// symmetric refusal: a ManagedOnly kind can never be switched to tui, because it has no pane
// program to launch. The old writer (tmux) must never be touched — the refusal has to happen
// before the stop step, or a real managed session would be killed for a switch that then fails.
func TestHandleSessionDriverRejectsTUITargetForManagedOnlyKind(t *testing.T) {
	logPath := fakeTmux(t)
	const name = "driver_lcpp"
	// Driver: managed only needs to differ from the "tui" target so the handler doesn't
	// short-circuit on "already there" — the refusal this test pins fires on Caps.ManagedOnly
	// alone, before the handler ever touches a runtime.
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindLcpp, Driver: session.DriverManaged})

	rec := postDriver(t, name, `{"driver":"tui"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "driver_unsupported") {
		t.Fatalf("status=%d body=%s, want 400 driver_unsupported", rec.Code, rec.Body.String())
	}
	log, _ := os.ReadFile(logPath)
	if strings.Contains(string(log), "kill-session") {
		t.Fatalf("refusal must happen before stopping the old writer, tmux log=%q", log)
	}
	m, _ := session.ReadMeta(name)
	if m.DriverKind() != session.DriverManaged {
		t.Fatalf("driver changed on refused switch: %q", m.Driver)
	}
}

// TestManagedOnlyCapDoesNotAffectExistingKinds is the registry-wide negative control: Caps's
// zero value has to mean "has a terminal route" for every kind but lcpp, or agents.Caps{}
// literals like shellAgent/ssmAgent's (agent_shell_ssm.go) would silently pick up
// ManagedOnly, and every kind that predates ADR 0093 would start refusing a tui target.
func TestManagedOnlyCapDoesNotAffectExistingKinds(t *testing.T) {
	for _, k := range []string{
		session.KindClaude, session.KindOpencode, session.KindCodex, session.KindCursor,
		session.KindKiro, session.KindAgy, session.KindCopilot, session.KindShell, session.KindSSM,
	} {
		if AgentOf(k).Caps().ManagedOnly {
			t.Fatalf("%s: Caps().ManagedOnly = true, want false", k)
		}
	}
	if !AgentOf(session.KindLcpp).Caps().ManagedOnly {
		t.Fatalf("lcpp: Caps().ManagedOnly = false, want true")
	}
}

// TestHandleSessionDriverKiroManagedToTUIUnaffected is the behavioral negative control named in
// the task: kiro can run on either driver (docs/log/43 Track A2), and a managed->tui switch —
// the direction my new ManagedOnly gate could wrongly refuse — must still succeed exactly as it
// did before this change (kiro's Caps().ManagedOnly is false, the zero value).
func TestHandleSessionDriverKiroManagedToTUIUnaffected(t *testing.T) {
	logPath := fakeTmux(t)
	const name = "driver_kiro"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindKiro, Driver: session.DriverManaged})

	rec := postDriver(t, name, `{"driver":"tui"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("kiro managed->tui status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	log, _ := os.ReadFile(logPath)
	if !strings.Contains(string(log), "new-session") {
		t.Fatalf("kiro managed->tui did not relaunch the tui pane, tmux log=%q", log)
	}
}
