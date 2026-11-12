package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// End to end against a real tmux: the attach client's device queries never reach the socket,
// its colour queries still do, so the Console has nothing to answer late.
func TestHandlePTY_DropsTmuxAttachQueries(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("AF_TMUX_SOCKET", "af-test-3981-"+strconv.Itoa(os.Getpid()))
	t.Setenv("AF_TERMINAL_HISTORY_DIR", t.TempDir())
	const name = "af-test-queries"
	if out, err := tmuxx.Cmd("new-session", "-d", "-s", session.TmuxName(name), "sleep 60").CombinedOutput(); err != nil {
		t.Skipf("tmux new-session: %v %s", err, out)
	}
	defer func() { _ = tmuxx.Cmd("kill-server").Run() }()

	srv := httptest.NewServer(http.HandlerFunc(handlePTY))
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"?session="+name, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	var got bytes.Buffer
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(got.String(), "\x1b]11;?") && time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v (so far %q)", err, got.String())
		}
		if mt == websocket.BinaryMessage {
			got.Write(data)
		}
	}
	// Anything the client sends after its colour queries arrives in the same burst.
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if mt == websocket.BinaryMessage {
			got.Write(data)
		}
	}
	out := got.String()
	if !strings.Contains(out, "\x1b]11;?") {
		t.Fatalf("no OSC 11 colour query relayed; output %q", out)
	}
	for _, q := range tmuxx.AttachQueries {
		if strings.Contains(out, string(q)) {
			t.Fatalf("attach query %q relayed; output %q", q, out)
		}
	}
}
