package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// When the Agent REFUSES the upgrade, the CP must keep its status instead of masking
// it as 502 "cannot reach workspace agent terminal" — a reachable, correctly-answering
// Agent reported as unreachable is undiagnosable. (A stopped session is no longer one
// of these refusals: the Agent now accepts and closes cleanly, see
// TestTerminalWebSocketForwardsAgentCloseFrame. This still covers every genuine
// refusal — bad name, auth — and 409 stands in for them here.) Note the status only
// ever reaches a human: browsers do not expose a failed handshake's status to JS.
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

// The bridge must pass the Agent's CLOSE frame through. gorilla answers the close
// handshake itself and surfaces it to relay() as a CloseError, so without explicit
// forwarding the browser only ever saw an abnormal 1006 — and 1006 is what the
// Console renders as "[disconnected]". That erased the whole distinction between "the
// session ended" (1000 + reason) and "the connection broke", which is why a stopped
// session's finite replay looked like a broken terminal.
func TestTerminalWebSocketForwardsAgentCloseFrame(t *testing.T) {
	const reason = "session stopped"
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, reason))
		// Stay up until the peer's close handshake comes back, so the handler does not
		// tear the connection down underneath the frame it just wrote.
		_, _, _ = c.ReadMessage()
	}))
	defer agent.Close()
	cp := newTerminalTestServer(t, agent.URL)

	wsURL := "ws" + cp.URL[len("http"):] + "/ws/terminal?session=gone&tenant=default"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, rerr := conn.ReadMessage()
	ce, ok := rerr.(*websocket.CloseError)
	if !ok {
		t.Fatalf("read = %v, want a close frame", rerr)
	}
	if ce.Code != websocket.CloseNormalClosure || ce.Text != reason {
		t.Fatalf("close = %d %q, want %d %q (1006/empty means the frame was dropped again)",
			ce.Code, ce.Text, websocket.CloseNormalClosure, reason)
	}
}
