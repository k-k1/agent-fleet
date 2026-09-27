package sessionx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// TestSSMLoginStatusJoinsWrappedURL pins #1025: the login modal reads the device URL off the
// session's tmux pane, and a narrow client (a phone, a split pane, or one attaching mid-login
// and resizing the window) wraps it. Without `capture-pane -J` the regex took the first
// wrapped row as the whole URL, so the modal's sign-in button opened a broken link.
func TestSSMLoginStatusJoinsWrappedURL(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	isolateAgentState(t)
	const name = "ssmwrap"
	const url = "https://device.sso.ap-northeast-1.amazonaws.com/?user_code=ABCD-EFGH"
	tn := session.TmuxName(name)
	// 30 columns: the URL (68 chars) wraps over three rows.
	out, err := tmuxx.Cmd("new-session", "-d", "-s", tn, "-x", "30", "-y", "24",
		"sh", "-c", fmt.Sprintf("printf '%%s\\n' 'Then enter the code:' 'ABCD-EFGH' %q; sleep 60", url)).CombinedOutput()
	if err != nil {
		t.Fatalf("new-session: %v\n%s", err, out)
	}
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindSSM})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /sessions/{name}/ssm-login", HandleSSMLoginStatus)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var got ssmLoginStatus
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, err := http.Get(srv.URL + "/sessions/" + name + "/ssm-login")
		if err != nil {
			t.Fatal(err)
		}
		got = ssmLoginStatus{}
		err = json.NewDecoder(res.Body).Decode(&got)
		res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		// Wait for printf to reach the pane: the phase stays "pending" until the URL shows.
		if got.Phase != "pending" || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got.Phase != "authorize" || got.URL != url || got.Code != "ABCD-EFGH" {
		t.Fatalf("ssm-login = %+v, want phase authorize, url %q, code ABCD-EFGH", got, url)
	}
	if strings.Contains(got.URL, "\n") {
		t.Fatalf("url carries a newline: %q", got.URL)
	}
}
