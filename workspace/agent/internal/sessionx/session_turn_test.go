package sessionx

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// fakeTmux installs a tmux stub that logs every invocation and answers the
// primitives the /turn tui route touches (has-session / list-panes / load-buffer /
// capture-pane). Returns the log path.
func fakeTmux(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	logPath := filepath.Join(bin, "tmux.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TMUX_TEST_LOG"
case "$1" in
  has-session) exit 0 ;;
  list-panes) printf '1 %%7\n' ;;
  capture-pane) printf '\n' ;;
  load-buffer) /bin/cat > /dev/null ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("TMUX_TEST_LOG", logPath)
	t.Setenv("AGENT_INPUT_SUBMIT_DELAY_MS", "0")
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	// HOME alone leaves CLAUDE_CONFIG_DIR / CODEX_HOME / COPILOT_HOME pointing at the
	// real trees, and a session launch materializes the MCP registry into them
	// (session_tmux.go / startManagedSession). See isolateAgentConfigDirs.
	isolateAgentConfigDirs(t)
	return logPath
}

func postTurn(t *testing.T, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/turn", strings.NewReader(body))
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	HandleSessionTurn(rec, req)
	return rec
}

// On tui, start and steer both collapse into the same type+submit path as /input {prompt}
// (bracketed paste for codex) — a regression guard that input reliability does not change
// when the Console moves to the semantic endpoints.
func TestHandleSessionTurnStartTypesPrompt(t *testing.T) {
	logPath := fakeTmux(t)
	const name = "turn_codex"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex})

	for _, op := range []string{"start", "steer"} {
		t.Run(op, func(t *testing.T) {
			if err := os.WriteFile(logPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			rec := postTurn(t, name, `{"op":"`+op+`","prompt":"do the thing"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			got, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), "paste-buffer -p -d -b af-prompt-"+name+"-") ||
				!strings.Contains(string(got), "send-keys -t %7 Enter") {
				t.Fatalf("tmux commands = %q, want bracketed paste + Enter", got)
			}
		})
	}
}

// On tui, interrupt collapses into Escape (the semantic form of the chat stop button). It
// must not mark working: no Stop hook fires, so the session would stick at "working".
func TestHandleSessionTurnInterruptSendsEscape(t *testing.T) {
	logPath := fakeTmux(t)
	const name = "turn_stop"
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindCodex})

	rec := postTurn(t, name, `{"op":"interrupt"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "send-keys -t %7 Escape") {
		t.Fatalf("tmux commands = %q, want send-keys Escape", got)
	}
	if st, ok := status.Read(session.UUID(dir, name)); ok && st.State == "working" {
		t.Fatal("interrupt must not mark the session working")
	}
}

// start/steer while a question is pending is 409, for the same reason as on /input: it would
// misanswer the TUI modal.
func TestHandleSessionTurnQuestionPendingGate(t *testing.T) {
	fakeTmux(t)
	const name = "turn_auq"
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindClaude})
	status.Persist(session.UUID(dir, name), "question")

	rec := postTurn(t, name, `{"op":"start","prompt":"free text"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "question_pending") {
		t.Fatalf("status = %d, body = %s, want 409 question_pending", rec.Code, rec.Body.String())
	}
}

// /turn and /respond on a managed session whose kind has no registered driver (claude) fail
// honestly with 501. opencode/codex are registered, so where the runtime is unavailable they
// fall to 502 runtime_failed — reproduced deterministically with the disable flag.
func TestHandleSessionTurnManagedUnavailable(t *testing.T) {
	fakeTmux(t)
	const name = "turn_managed"
	// claude has no managed driver (out of scope per ADR 0015) → 501.
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude, Driver: session.DriverManaged})

	rec := postTurn(t, name, `{"op":"start","prompt":"x"}`)
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "driver_unavailable") {
		t.Fatalf("status = %d, body = %s, want 501 driver_unavailable", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/respond",
		strings.NewReader(`{"id":"i1","decision":"answer","answers":[{"options":[0]}]}`))
	req.SetPathValue("name", name)
	rec = httptest.NewRecorder()
	HandleSessionRespond(rec, req)
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "driver_unavailable") {
		t.Fatalf("respond status = %d, body = %s, want 501 driver_unavailable", rec.Code, rec.Body.String())
	}
}

// managed opencode is enabled, so /turn heads for the ThreadHandle and, where the runtime
// cannot be started, fails honestly with 502 runtime_failed rather than 501.
func TestHandleSessionTurnManagedOpencodeNeedsRuntime(t *testing.T) {
	fakeTmux(t)
	t.Setenv("AF_OPENCODE_SERVE_DISABLE", "1")
	const name = "turn_mng_oc"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindOpencode, Driver: session.DriverManaged})

	rec := postTurn(t, name, `{"op":"start","prompt":"x"}`)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "runtime_failed") {
		t.Fatalf("status = %d, body = %s, want 502 runtime_failed", rec.Code, rec.Body.String())
	}
}

// /respond on a tui session is explicitly unsupported (the Console uses keys/seq there).
func TestHandleSessionRespondUnsupportedForTUI(t *testing.T) {
	fakeTmux(t)
	const name = "respond_tui"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})

	req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/respond",
		strings.NewReader(`{"id":"i1","decision":"answer"}`))
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	HandleSessionRespond(rec, req)
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "respond_unsupported") {
		t.Fatalf("status = %d, body = %s, want 501 respond_unsupported", rec.Code, rec.Body.String())
	}
}

// Validation of op / decision / id.
func TestHandleSessionTurnValidation(t *testing.T) {
	fakeTmux(t)
	const name = "turn_valid"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})

	if rec := postTurn(t, name, `{"op":"dance"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad op: status = %d, want 400", rec.Code)
	}
	if rec := postTurn(t, name, `{"op":"start","prompt":"  "}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty prompt: status = %d, want 400", rec.Code)
	}
	if rec := postTurn(t, "no_such", `{"op":"start","prompt":"x"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session: status = %d, want 404", rec.Code)
	}
}

// Driver validation on the create API (docs/log/27 P2/P3): managed is accepted only for
// kinds with a registered driver (opencode/codex), and an unregistered kind (claude and the
// like) is refused explicitly before any side effect. An unknown value is bad_driver. An
// accepted managed create fails with 502 where the runtime is unavailable, and never creates
// a tmux session.
func TestCreateSessionManagedDriverGate(t *testing.T) {
	logPath := fakeTmux(t)
	// allocSessionName probes name occupancy with tmux has-session. The default stub always
	// exits 0 (= every name taken) and would loop forever, so swap in one that reports absent
	// (exit 1).
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TMUX_TEST_LOG"
case "$1" in
  has-session) exit 1 ;;
  list-panes) printf '1 %%7\n' ;;
  load-buffer) /bin/cat > /dev/null ;;
esac
`
	if err := os.WriteFile(filepath.Join(filepath.Dir(logPath), "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_OPENCODE_SERVE_DISABLE", "1")

	// claude is out of managed's scope (ADR 0015) → 400 driver_unsupported.
	req := httptest.NewRequest(http.MethodPost, "/sessions",
		strings.NewReader(`{"kind":"claude","driver":"managed"}`))
	rec := httptest.NewRecorder()
	HandleCreateSession(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "driver_unsupported") {
		t.Fatalf("claude managed: status = %d, body = %s, want 400 driver_unsupported", rec.Code, rec.Body.String())
	}

	// opencode is enabled: validation passes and the disabled runtime turns it into a 502.
	req = httptest.NewRequest(http.MethodPost, "/sessions",
		strings.NewReader(`{"kind":"opencode","driver":"managed"}`))
	rec = httptest.NewRecorder()
	HandleCreateSession(rec, req)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "runtime_failed") {
		t.Fatalf("opencode managed: status = %d, body = %s, want 502 runtime_failed", rec.Code, rec.Body.String())
	}
	// No tmux session was created: managed sessions have no pane.
	if got, _ := os.ReadFile(logPath); strings.Contains(string(got), "new-session") {
		t.Fatalf("managed create must not spawn tmux, log = %q", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/sessions",
		strings.NewReader(`{"kind":"opencode","driver":"warp"}`))
	rec = httptest.NewRecorder()
	HandleCreateSession(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_driver") {
		t.Fatalf("status = %d, body = %s, want 400 bad_driver", rec.Code, rec.Body.String())
	}
}

// wireSession passes driver straight through to the wire ("" is dropped by omitempty) so the
// Console can decide on paneless (managed) rendering from the session list alone.
func TestWireSessionCarriesDriver(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	m := session.Meta{Name: "s1", Dir: "/tmp/x", Kind: session.KindOpencode, Driver: session.DriverManaged}
	if got := wireSession(m, false).Driver; got != session.DriverManaged {
		t.Fatalf("wire driver = %q, want managed", got)
	}
	m.Driver = ""
	if got := wireSession(m, false).Driver; got != "" {
		t.Fatalf("wire driver = %q, want empty (tui default)", got)
	}
	if m.DriverKind() != session.DriverTUI {
		t.Fatalf("DriverKind() = %q, want tui", m.DriverKind())
	}
}
