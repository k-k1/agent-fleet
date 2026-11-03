package sessionx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
)

func postBackgroundStop(name, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/background-stop", strings.NewReader(body))
	r.SetPathValue("name", name)
	w := httptest.NewRecorder()
	HandleSessionBackgroundStop(w, r)
	return w
}

// The guards in front of claude.StopBackgroundShell, and what a session without a claude pane
// answers: gone, never a lookup anywhere else.
func TestBackgroundStopGuards(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	testguard.IsolateTmux(t) // the pane lookup must never reach the workspace's live tmux server
	session.WriteMeta(session.Meta{Name: "bgc", Dir: t.TempDir(), Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "bgx", Dir: t.TempDir(), Kind: "codex"})
	session.WriteMeta(session.Meta{Name: "bgm", Dir: t.TempDir(), Kind: session.KindClaude, Driver: session.DriverManaged})

	cases := []struct {
		name, session, body string
		code                int
	}{
		{"pid 1 refused", "bgc", `{"pid":1,"start":5}`, http.StatusBadRequest},
		{"missing pid refused", "bgc", `{}`, http.StatusBadRequest},
		{"unknown session", "nosuch", `{"pid":4242,"start":5}`, http.StatusNotFound},
		{"codex: gone", "bgx", `{"pid":4242,"start":5}`, http.StatusConflict},
		{"managed claude: gone", "bgm", `{"pid":4242,"start":5}`, http.StatusConflict},
		{"no pane: gone", "bgc", `{"pid":4242,"start":5}`, http.StatusConflict},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if w := postBackgroundStop(c.session, c.body); w.Code != c.code {
				t.Fatalf("status=%d body=%s, want %d", w.Code, w.Body.String(), c.code)
			}
		})
	}
}
