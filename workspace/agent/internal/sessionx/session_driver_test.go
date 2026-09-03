package sessionx

// POST /sessions/{name}/driver の回帰テスト。単一 writer のため旧ドライバを
// stop してから meta を反転し、新ドライバで resume する順序と、busy 時の排他拒否を
// tmux スタブ上で検証する。

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

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
