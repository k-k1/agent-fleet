package sessionx

// Regression tests for POST /sessions/{name}/driver. Because there is only ever one writer,
// the order is: stop the old driver, flip meta, resume under the new one. That order and the
// exclusion refusal while busy are both verified over a tmux stub.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

func postDriver(t *testing.T, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/driver", strings.NewReader(body))
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	HandleSessionDriver(rec, req)
	return rec
}

func TestHandleSessionDriverValidationAndCapability(t *testing.T) {
	fakeTmux(t)
	if rec := postDriver(t, "bad/name", `{"driver":"managed"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad name status = %d", rec.Code)
	}
	if rec := postDriver(t, "missing", `{"driver":"warp"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_driver") {
		t.Fatalf("bad driver status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := postDriver(t, "missing", `{"driver":"managed"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", rec.Code)
	}

	const name = "driver_claude"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})
	if rec := postDriver(t, name, `{"driver":"managed"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "driver_unsupported") {
		t.Fatalf("unsupported status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleSessionDriverRejectsBusyBeforeStoppingWriter(t *testing.T) {
	logPath := fakeTmux(t)
	const name = "driver_busy"
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindCodex})
	status.Persist(session.UUID(dir, name), "working")

	rec := postDriver(t, name, `{"driver":"managed"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "busy_switch") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	log, _ := os.ReadFile(logPath)
	if strings.Contains(string(log), "kill-session") {
		t.Fatalf("busy switch must leave the writer alive, tmux log=%q", log)
	}
	m, _ := session.ReadMeta(name)
	if m.DriverKind() != session.DriverTUI {
		t.Fatalf("driver changed on busy rejection: %q", m.Driver)
	}
}

func TestHandleSessionDriverManagedFailureRollsBackMeta(t *testing.T) {
	logPath := fakeTmux(t)
	t.Setenv("AF_CODEX_APP_SERVER_DISABLE", "1")
	const name = "driver_rollback"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex})

	rec := postDriver(t, name, `{"driver":"managed"}`)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "runtime_failed") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	m, _ := session.ReadMeta(name)
	if m.DriverKind() != session.DriverTUI || m.Driver != "" {
		t.Fatalf("failed switch did not restore tui meta: %+v", m)
	}
	log, _ := os.ReadFile(logPath)
	if !strings.Contains(string(log), "kill-session") {
		t.Fatalf("old tui writer was not stopped before managed resume: %q", log)
	}
}

func TestHandleSessionDriverSwitchesManagedToTUI(t *testing.T) {
	logPath := fakeTmux(t)
	const name = "driver_to_tui"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged})

	rec := postDriver(t, name, `{"driver":"tui"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	m, _ := session.ReadMeta(name)
	if m.Driver != "" || m.DriverKind() != session.DriverTUI {
		t.Fatalf("driver meta = %q, want tui encoded as empty", m.Driver)
	}
	log, _ := os.ReadFile(logPath)
	text := string(log)
	killAt, startAt := strings.Index(text, "kill-session"), strings.Index(text, "new-session")
	if killAt < 0 || startAt < 0 || killAt > startAt {
		t.Fatalf("want stop before tui resume, tmux log=%q", text)
	}
}

// A running managed codex session is not switched to Terminal: the TUI could not open the
// thread the running session holds, so the switch is refused before anything is stopped.
func TestHandleSessionDriverCodexToTUIRefusesARunningSession(t *testing.T) {
	logPath := fakeTmux(t)
	prev := switchSourceAlive
	switchSourceAlive = func(session.Meta) bool { return true }
	t.Cleanup(func() { switchSourceAlive = prev })
	const name = "driver_codex_running"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged})

	rec := postDriver(t, name, `{"driver":"tui"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), errCodeCodexStopFirst) {
		t.Fatalf("status=%d body=%s, want 409 %s", rec.Code, rec.Body.String(), errCodeCodexStopFirst)
	}
	if m, _ := session.ReadMeta(name); m.DriverKind() != session.DriverManaged {
		t.Fatalf("driver changed on refusal: %q", m.Driver)
	}
	if log, _ := os.ReadFile(logPath); strings.Contains(string(log), "kill-session") || strings.Contains(string(log), "new-session") {
		t.Fatalf("a refused switch touched tmux: %q", log)
	}
}

// A stopped codex session whose thread the shared app-server has not unloaded yet is refused
// with codex_releasing, both by the switch and by a Terminal /start, and no pane is opened: the
// direct TUI would stop on codex's "open in another app" screen.
func TestCodexTerminalLaunchRefusedWhileTheThreadIsLoaded(t *testing.T) {
	logPath := fakeTmux(t)
	t.Setenv("AF_CODEX_APP_SERVER_ADDR", fakeLoadedListServer(t, "thr-held"))
	dir := t.TempDir()
	const name = "driver_codex_releasing"
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindCodex, Driver: session.DriverManaged})
	codex.RememberSid(session.UUID(dir, name), "thr-held")

	rec := postDriver(t, name, `{"driver":"tui"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "codex_releasing") {
		t.Fatalf("switch: status=%d body=%s, want 409 codex_releasing", rec.Code, rec.Body.String())
	}
	if m, _ := session.ReadMeta(name); m.DriverKind() != session.DriverManaged {
		t.Fatalf("driver changed on refusal: %q", m.Driver)
	}
	if log, _ := os.ReadFile(logPath); strings.Contains(string(log), "new-session") {
		t.Fatalf("a pane was opened for a thread still loaded: %q", log)
	}

	// The same session already on Terminal and stopped: /start is refused the same way.
	UpdateSessionMeta(name, func(m *session.Meta) bool { m.Driver = ""; return true })
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TMUX_TEST_LOG\"\ncase \"$1\" in\n  has-session) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/start", nil)
	req.SetPathValue("name", name)
	rec = httptest.NewRecorder()
	HandleStartSession(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "codex_releasing") {
		t.Fatalf("/start: status=%d body=%s, want 409 codex_releasing", rec.Code, rec.Body.String())
	}
	if log, _ := os.ReadFile(logPath); strings.Contains(string(log), "new-session") {
		t.Fatalf("/start opened a pane for a thread still loaded: %q", log)
	}
}

// fakeLoadedListServer is a stand-in codex app-server that reports the given threads loaded;
// the probe must send nothing but initialize and thread/loaded/list.
func fakeLoadedListServer(t *testing.T, loaded ...string) string {
	t.Helper()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			var m map[string]any
			if c.ReadJSON(&m) != nil {
				return
			}
			switch m["method"] {
			case "initialize":
				_ = c.WriteJSON(map[string]any{"id": m["id"], "result": map[string]any{}})
			case "initialized":
			case "thread/loaded/list":
				_ = c.WriteJSON(map[string]any{"id": m["id"], "result": map[string]any{"data": loaded, "nextCursor": nil}})
			default:
				t.Errorf("the probe sent %v: it must only read thread/loaded/list", m["method"])
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}
