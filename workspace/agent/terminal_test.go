package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// A STOPPED session must come back as a clean, LABELLED end-of-stream — never as a
// refused handshake. A browser cannot read the status or the body of a failed
// WebSocket upgrade, so the old 409 reached the Console as an indistinguishable
// abnormal drop and the pane rendered "[disconnected]": an ordinary stopped session
// looked like a broken terminal. That is what a container restart did to every
// stopped session at once, because it empties the /tmp history ring and the
// no-history branch was the one that refused. Both branches (with and without a
// replay) must upgrade and close 1000 carrying ptyCloseSessionStopped, which is the
// only signal the client can key on.
func TestHandlePTY_StoppedSessionClosesCleanlyWithReason(t *testing.T) {
	for _, tc := range []struct {
		name    string
		history string // "" = nothing recorded (the post-restart case)
	}{
		{name: "no history", history: ""},
		{name: "with history", history: "previous screen\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("AF_TERMINAL_HISTORY_DIR", dir)
			// A name nothing can be running under, so HasSession is false for real.
			const name = "af-test-stopped"
			if tc.history != "" {
				if err := os.WriteFile(filepath.Join(dir, name+".ansi"), []byte(tc.history), 0o600); err != nil {
					t.Fatalf("seed history: %v", err)
				}
			}

			srv := httptest.NewServer(http.HandlerFunc(handlePTY))
			defer srv.Close()
			wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "?session=" + name
			conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				t.Fatalf("dial: %v (response %v) — a stopped session must still UPGRADE; "+
					"refusing the handshake is invisible to a browser", err, resp)
			}
			defer conn.Close()

			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			for {
				mt, data, rerr := conn.ReadMessage()
				if rerr != nil {
					ce, ok := rerr.(*websocket.CloseError)
					if !ok {
						t.Fatalf("read = %v, want a close frame", rerr)
					}
					if ce.Code != websocket.CloseNormalClosure {
						t.Fatalf("close code = %d, want %d — anything else renders as [disconnected]",
							ce.Code, websocket.CloseNormalClosure)
					}
					if ce.Text != ptyCloseSessionStopped {
						t.Fatalf("close reason = %q, want %q (wire contract with term.ts)",
							ce.Text, ptyCloseSessionStopped)
					}
					return
				}
				// The only frame before the close is the replay, when there is one.
				if mt == websocket.BinaryMessage && string(data) != tc.history {
					t.Fatalf("replayed %q, want %q", data, tc.history)
				}
			}
		})
	}
}
