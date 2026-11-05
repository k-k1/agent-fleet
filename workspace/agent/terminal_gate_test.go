package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/lifegate"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// Typing in the browser terminal is an input path like any other (ADR 0115 decision 11.1): while
// a route switch holds the session's gate the bytes wait, and they reach the process only after
// it is released. Without the gate they went straight to the old process mid-switch.
func TestHandlePTY_InputWaitsForTheLifecycleGate(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	// A short socket name: the path of a Unix socket has a platform length limit that a name built
	// from the test's own name overran under the test guard.
	t.Setenv("AF_TMUX_SOCKET", "tg")
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	t.Cleanup(func() { _ = tmuxx.Cmd("kill-server").Run() })
	name := "tgate1"
	out := filepath.Join(t.TempDir(), "typed")
	if o, err := tmuxx.Cmd("new-session", "-d", "-s", session.TmuxName(name), "cat > "+out).CombinedOutput(); err != nil {
		t.Fatalf("starting the tmux session: %v %s", err, o)
	}
	srv := httptest.NewServer(http.HandlerFunc(handlePTY))
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"?session="+name, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	x, err := lifegate.Lock(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{"type": "input", "data": "hello-from-the-member\n"})
	if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	if data, _ := os.ReadFile(out); strings.Contains(string(data), "hello") {
		x.Unlock()
		t.Fatal("terminal input reached the process while the gate was held")
	}
	x.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if data, _ := os.ReadFile(out); strings.Contains(string(data), "hello-from-the-member") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the input never arrived after the gate was released")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
