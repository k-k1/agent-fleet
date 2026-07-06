package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// The app-level heartbeat is the only liveness signal that survives the CP proxy
// (which relays data frames but swallows protocol ping/pong), so the Agent must echo a
// text {"type":"pong"} for every {"type":"ping"} — that's how the client detects a
// dead connection and re-attaches. Drive handlePTY over a real WebSocket to prove it.
func TestHandlePTY_PingPong(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(handlePTY))
	defer srv.Close()

	// session="" opens a login shell, so the test needs no tmux session.
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`)); err != nil {
		t.Fatalf("write ping: %v", err)
	}

	// The shell also streams startup output as binary frames — read past those and
	// assert the first TEXT frame is our pong.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v (never saw a pong)", err)
		}
		if mt != websocket.TextMessage {
			continue // binary PTY output — skip
		}
		var m struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("pong not JSON: %q", data)
		}
		if m.Type != "pong" {
			t.Fatalf("got text frame type %q, want pong", m.Type)
		}
		return // pong received
	}
}
