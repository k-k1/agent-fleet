package sessionx

// Tests for the arming side of stop-after-turn (docs/log/85). What the reconciler does with
// the arm is pinned in internal/chatx; here it is the arm itself: the toggle, the wire, and
// the paths that have to consume it.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func postStopAfterTurn(t *testing.T, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/stop-after-turn", strings.NewReader(body))
	r.SetPathValue("name", name)
	w := httptest.NewRecorder()
	HandleSessionStopAfterTurn(w, r)
	return w
}

func stopArmField(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		StopAfterTurnAt string `json:"stopAfterTurnAt"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad response body %s: %v", w.Body.String(), err)
	}
	return out.StopAfterTurnAt
}

func TestStopAfterTurnArmAndRelease(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const name = "armed1"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})

	w := postStopAfterTurn(t, name, `{"on":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("arm status=%d body=%s", w.Code, w.Body.String())
	}
	if stopArmField(t, w) == "" {
		t.Fatal("arming must answer with the instant it was armed at")
	}
	m, _ := session.ReadMeta(name)
	if _, live := session.StopArmedAt(m, time.Now()); !live {
		t.Fatalf("the arm was not persisted: %q", m.StopAfterTurnAt)
	}

	if got := stopArmField(t, postStopAfterTurn(t, name, `{"on":false}`)); got != "" {
		t.Fatalf("releasing must clear the arm, got %q", got)
	}
	m, _ = session.ReadMeta(name)
	if m.StopAfterTurnAt != "" {
		t.Fatalf("the release was not persisted: %q", m.StopAfterTurnAt)
	}

	if got := postStopAfterTurn(t, "nosuch", `{"on":true}`); got.Code != http.StatusNotFound {
		t.Fatalf("arming an unknown session = %d, want 404", got.Code)
	}
}

// The list is polled every few seconds, and it writes the meta back as a side effect. Without
// the arm being carried through that merge, arming would look like it worked and then quietly
// stop being honoured — worse than a button that does nothing, because the row still says the
// session is going to stop.
func TestStopArmSurvivesALifecycleWriteback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const name = "armed2"
	stale := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(stale)

	postStopAfterTurn(t, name, `{"on":true}`)
	// stale is the snapshot a list request read BEFORE the arm was pressed.
	stale.StoppedAt = time.Now().Format(time.RFC3339)
	if got := WriteSessionMetaKeepingLock(stale); got.StopAfterTurnAt == "" {
		t.Fatal("a stale lifecycle write rolled the arm back")
	}
}

// Every fold consumes the arm. Left behind, it rides through the resume and stops the session
// again at the end of a turn nobody armed.
func TestFoldingConsumesTheStopArm(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name string
		fold func(session.Meta)
	}{
		{"halt", func(m session.Meta) {
			if _, err := haltSessionMeta(m); err != nil {
				t.Fatalf("halt: %v", err)
			}
		}},
		{"archive", func(m session.Meta) {
			r := httptest.NewRequest(http.MethodPost, "/sessions/"+m.Name+"/archive", nil)
			r.SetPathValue("name", m.Name)
			HandleArchiveSession(httptest.NewRecorder(), r)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := "armed-" + tc.name
			// No tmux session exists for these names, so the fold takes its "already
			// stopped" path — which is exactly the path that must still consume the arm.
			session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})
			postStopAfterTurn(t, name, `{"on":true}`)
			m, _ := session.ReadMeta(name)
			tc.fold(m)
			got, _ := session.ReadMeta(name)
			if got.StopAfterTurnAt != "" {
				t.Fatalf("%s left the arm behind: %q", tc.name, got.StopAfterTurnAt)
			}
		})
	}
}

// "Stop when you are done" is said about the work in flight; a new instruction is not part of
// it. Cancelling is also the safe direction — being wrong costs a session that keeps running,
// not one that vanished mid-instruction.
func TestNewPromptReleasesTheStopArm(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const name = "armed3"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})
	postStopAfterTurn(t, name, `{"on":true}`)

	cancelStopArmOnNewPrompt(name)
	if m, _ := session.ReadMeta(name); m.StopAfterTurnAt != "" {
		t.Fatalf("a new instruction must release the arm: %q", m.StopAfterTurnAt)
	}
}

// An expired arm has to disappear from the row as well: a badge promising a stop that nothing
// will act on is worse than no badge at all (the trap the keep-awake pin's render check
// already answers).
func TestExpiredStopArmIsNotOnTheWire(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "armed4", Dir: t.TempDir(), Kind: session.KindClaude}
	m.StopAfterTurnAt = time.Now().Add(-session.StopArmMaxAge - time.Hour).Format(time.RFC3339)
	if got := stopArmVisible(m); got != "" {
		t.Fatalf("expired arm is still on the wire: %q", got)
	}
	m.StopAfterTurnAt = time.Now().Format(time.RFC3339)
	if got := stopArmVisible(m); got == "" {
		t.Fatal("a live arm must be on the wire")
	}
}

// A create can arm the session up front (docs/log/85): the CP scheduler uses it for a
// schedule with stop_after_run. It rides the create rather than a POST that follows,
// because the arm has to be on disk BEFORE the initial prompt is delivered — a prompt
// arriving after an arm is exactly what releases it, so the two orders are not equivalent.
func TestCreateSessionCanArmStopAfterTurn(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", HandleCreateSession)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, tc := range []struct {
		name string
		arm  bool
	}{{"armed", true}, {"plain", false}} {
		t.Run(tc.name, func(t *testing.T) {
			var created session.Session
			do(t, srv, "POST", "/sessions", map[string]any{
				"dir": home, "kind": "shell", "stop_after_turn": tc.arm,
			}, http.StatusCreated, &created)
			defer exec.Command("tmux", "kill-session", "-t", session.TmuxName(created.Name)).Run()

			m, ok := session.ReadMeta(created.Name)
			if !ok {
				t.Fatal("meta not persisted")
			}
			if _, live := session.StopArmedAt(m, time.Now()); live != tc.arm {
				t.Fatalf("armed=%v, want %v (meta %q)", live, tc.arm, m.StopAfterTurnAt)
			}
			if (created.StopAfterTurnAt != "") != tc.arm {
				t.Fatalf("wire stopAfterTurnAt = %q, want armed=%v", created.StopAfterTurnAt, tc.arm)
			}
		})
	}
}
