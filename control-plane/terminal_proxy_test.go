package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
)

func newTerminalTestServer(t *testing.T, agentURL string) *httptest.Server {
	t.Helper()
	env := newBrowserTestEnv(t, browserTestRuntime{endpoint: agentURL, token: "agent-secret", state: "running"})
	proxy := newAgentProxyAPI(env.mgr)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/terminal", proxy.withResolved(proxy.terminal))
	cp := httptest.NewServer(mux)
	t.Cleanup(cp.Close)
	return cp
}

// The Agent refuses a stopped session with no replay (409 "session not running and no
// terminal history"). The CP used to discard that response and answer 502 "cannot reach
// workspace agent terminal" — a reachable, correctly-answering Agent reported as
// unreachable, which is undiagnosable from the Console (browsers do not expose a failed
// WebSocket handshake's status to JS at all).
func TestTerminalWebSocketPreservesAgentHandshakeError(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer agent-secret" {
			t.Errorf("Agent Authorization = %q", got)
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="agent-secret"`)
		http.Error(w, "session not running and no terminal history", http.StatusConflict)
	}))
	defer agent.Close()
	cp := newTerminalTestServer(t, agent.URL)

	wsURL := "ws" + cp.URL[len("http"):] + "/ws/terminal?session=gone&tenant=default"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil || resp == nil {
		t.Fatalf("stopped-session dial = err %v response %v", err, resp)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (502 means the Agent's answer was masked again)", resp.StatusCode)
	}
	if string(body) != "session not running and no terminal history\n" {
		t.Fatalf("body = %q", body)
	}
	// The Agent's own auth challenge is between CP and Agent — relaying it would invite
	// the browser to answer with the Agent bearer.
	if got := resp.Header.Get("WWW-Authenticate"); got != "" {
		t.Fatalf("Agent challenge leaked to Console: %q", got)
	}
}

// A genuinely unreachable Agent still gets the blanket 502: there is no response to
// relay, and that is the ONE case the old message was actually describing.
func TestTerminalWebSocketUnreachableAgentIsBadGateway(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	agentURL := agent.URL
	agent.Close() // nothing is listening now — dial fails with no response
	cp := newTerminalTestServer(t, agentURL)

	wsURL := "ws" + cp.URL[len("http"):] + "/ws/terminal?session=any&tenant=default"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil || resp == nil {
		t.Fatalf("dead-agent dial = err %v response %v", err, resp)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}
