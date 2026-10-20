package sessionx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// refuseKind installs a KindStartRefusal that refuses exactly kind with the code the Agent uses
// for a full home.
func refuseKind(t *testing.T, kind string) {
	t.Helper()
	prev := KindStartRefusal
	KindStartRefusal = func(k string) *SpawnRefusal {
		if k != kind {
			return nil
		}
		return &SpawnRefusal{Status: http.StatusConflict, Code: "cli_install_no_space", Message: kind + " could not be installed"}
	}
	t.Cleanup(func() { KindStartRefusal = prev })
}

func postCreateRec(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	HandleCreateSession(w, httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(body)))
	return w
}

// The refusal depends on state that changes, so it runs after the idempotency claim: a retry of
// a create that already succeeded replays its session, one still running is create_in_progress,
// and a durable key ends as refused with the outcome headers the Control Plane settles on.
func TestStartRefusalRunsAfterTheIdempotencyClaim(t *testing.T) {
	useCreateOutcomeDir(t)
	refuseKind(t, "codex")

	// A fresh create is refused with the code.
	w := postCreateRec(t, `{"kind":"codex","dir":"/tmp"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "cli_install_no_space") {
		t.Fatalf("fresh create = %d %s, want 409 cli_install_no_space", w.Code, w.Body)
	}

	// Done: the CLI vanished after the first create succeeded, the retry replays the session.
	if _, dup := createLedger.begin("k-done", false); dup {
		t.Fatal("begin")
	}
	createLedger.complete("k-done", []byte(`{"name":"already"}`))
	w = postCreateRec(t, `{"kind":"codex","dir":"/tmp","idempotency_key":"k-done"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "already") || w.Header().Get(createOutcomeHeader) != "created" {
		t.Fatalf("retry of a finished create = %d %s %v, want its 200 replay", w.Code, w.Body, w.Header())
	}

	// Inflight: the first create is still running.
	if _, dup := createLedger.begin("k-run", false); dup {
		t.Fatal("begin")
	}
	w = postCreateRec(t, `{"kind":"codex","dir":"/tmp","idempotency_key":"k-run"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "create_in_progress") {
		t.Fatalf("retry of a running create = %d %s, want create_in_progress", w.Code, w.Body)
	}

	// Durable: refused with the headers, final on retry.
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/sessions-idempotency/k-dur/reserve", nil)
	r.SetPathValue("key", "k-dur")
	HandleIdempotencyReserve(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("reserve = %d", rec.Code)
	}
	w = postCreateRec(t, `{"kind":"codex","dir":"/tmp","idempotency_key":"k-dur","idempotency_durable":true}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "cli_install_no_space") ||
		w.Header().Get(createOutcomeHeader) != "refused" || w.Header().Get(createKeyHeader) != "k-dur" {
		t.Fatalf("durable refused create = %d %s %v", w.Code, w.Body, w.Header())
	}
	w = postCreateRec(t, `{"kind":"codex","dir":"/tmp","idempotency_key":"k-dur","idempotency_durable":true}`)
	if !strings.Contains(w.Body.String(), "create_refused") {
		t.Fatalf("retry under a refused key = %d %s, want create_refused", w.Code, w.Body)
	}
}

// Resuming a stored conversation (POST /sessions/{name}/start) launches the CLI too: the typed
// refusal reaches the answer and nothing is launched.
func TestStartOfAStoredSessionIsRefusedWithTheCode(t *testing.T) {
	home := writebackEnv(t)
	refuseKind(t, session.KindCodex)
	const name = "resume1"
	session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindCodex, Driver: session.DriverManaged})
	launched := false
	useLockingDriver(t, session.KindCodex, &lockingDriver{onResume: func() { launched = true }})

	w := callHandler(HandleStartSession, http.MethodPost, name, "")
	var out struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != http.StatusConflict || out.Error.Code != "cli_install_no_space" || launched {
		t.Fatalf("start = %d %s (launched=%v), want 409 cli_install_no_space and no launch", w.Code, w.Body, launched)
	}

	// A kind that is not refused starts as before.
	other := "resume2"
	session.WriteMeta(session.Meta{Name: other, Dir: home, Kind: session.KindClaude, Driver: session.DriverManaged})
	if w := callHandler(HandleStartSession, http.MethodPost, other, ""); strings.Contains(w.Body.String(), "cli_install") {
		t.Fatalf("an unrefused kind got a refusal: %s", w.Body)
	}
}

// Recreate is refused before the old session is archived or killed.
func TestRecreateIsRefusedBeforeArchivingTheOldSession(t *testing.T) {
	home := writebackEnv(t)
	refuseKind(t, session.KindClaude)
	const name = "recref1"
	session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindClaude})
	w := callHandler(HandleRecreateSession, http.MethodPost, name, "")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "cli_install_no_space") {
		t.Fatalf("recreate = %d %s, want 409 cli_install_no_space", w.Code, w.Body)
	}
	if m, ok := session.ReadMeta(name); !ok || m.Archived {
		t.Fatalf("the old session was archived by a refused recreate: %+v", m)
	}
}

// Fork is refused before anything is created.
func TestForkIsRefusedBeforeCreatingAnything(t *testing.T) {
	home := writebackEnv(t)
	refuseKind(t, session.KindClaude)
	const name = "forkref1"
	session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindClaude})
	w := callHandler(HandleForkSession, http.MethodPost, name, "")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "cli_install_no_space") {
		t.Fatalf("fork = %d %s, want 409 cli_install_no_space", w.Code, w.Body)
	}
}

// The same for a Terminal (tmux) session: no tmux session is alive, so /start would launch.
func TestStartOfAStoredTerminalSessionIsRefused(t *testing.T) {
	home := writebackEnv(t)
	refuseKind(t, session.KindClaude)
	const name = "resume3"
	session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindClaude})
	w := callHandler(HandleStartSession, http.MethodPost, name, "")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "cli_install_no_space") {
		t.Fatalf("start = %d %s, want 409 cli_install_no_space", w.Code, w.Body)
	}
}

// Managed input of every kind (turn, input, carried answer) starts the runtime itself through
// Resume, without ensureSessionTmux: a stopped session whose CLI did not install is refused with
// the code there too, and the driver is never asked to start.
func TestManagedInputIsRefusedBeforeResume(t *testing.T) {
	home := writebackEnv(t)
	refuseKind(t, session.KindCodex)
	launched := false
	useLockingDriver(t, session.KindCodex, &lockingDriver{onResume: func() { launched = true }})
	const name = "mgrefuse1"
	session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindCodex, Driver: session.DriverManaged})

	for _, tc := range []struct {
		route string
		h     http.HandlerFunc
		body  string
	}{
		{"turn", HandleSessionTurn, `{"op":"start","prompt":"hi"}`},
		{"input", HandleSessionInput, `{"prompt":"hi"}`},
	} {
		w := callHandler(tc.h, http.MethodPost, name, tc.body)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "cli_install_no_space") || launched {
			t.Errorf("%s = %d %s (launched=%v), want 409 cli_install_no_space and no launch", tc.route, w.Code, w.Body, launched)
		}
	}

	// A carried answer on a stopped session goes through ensureSessionTmux; its typed refusal
	// must reach the answer as the code, not start_failed.
	if !status.PutCarried(session.UUID(home, name), status.Carried{Kind: "permission", Permission: "Bash · ls"}, "halt") {
		t.Fatal("carried not stored")
	}
	w := callHandler(HandleSessionCarriedAnswer, http.MethodPost, name, `{"decision":"continue"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "cli_install_no_space") {
		t.Errorf("carried answer = %d %s, want 409 cli_install_no_space", w.Code, w.Body)
	}
}
