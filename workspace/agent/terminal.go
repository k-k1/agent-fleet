package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

// Internal-only endpoint; the Control Plane is the sole client and enforces
// origin/auth upstream. Accept any origin here (VPC/docker-network bound).
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// ctrlMsg is the client->server text protocol. Server->client is raw binary
// (PTY bytes) to avoid UTF-8 framing issues on terminal output — except pongMsg, the
// one server->client text frame, sent in reply to a "ping" heartbeat.
type ctrlMsg struct {
	Type string `json:"type"`           // "input" | "resize" | "ping"
	Data string `json:"data,omitempty"` // for input
	Cols uint16 `json:"cols,omitempty"` // for resize
	Rows uint16 `json:"rows,omitempty"`
}

// pongMsg is the heartbeat reply. A text frame so the client's onmessage can tell it
// apart from binary PTY output and consume it without writing to the terminal grid.
var pongMsg = []byte(`{"type":"pong"}`)

// handlePTY bridges a browser terminal to a PTY. With ?session=<name> it
// attaches the matching tmux session; otherwise it opens a login shell
// (used for `claude /login` and ad-hoc commands).
func handlePTY(w http.ResponseWriter, r *http.Request) {
	session := r.URL.Query().Get("session")

	var cmd *exec.Cmd
	if session != "" {
		if !nameRe.MatchString(session) {
			http.Error(w, "invalid session name", http.StatusBadRequest)
			return
		}
		// Connect-only: attach to a RUNNING session, never start a stopped one. Merely
		// opening a session's chat/terminal (or a stale "alive" right after a Workspace
		// Start) used to auto-relaunch it via ensureSessionTmux; resuming is now explicit
		// (POST /sessions/{name}/start, driven by 再開して続ける / the ターミナル toggle).
		if !tmuxHasSession(tmuxName(session)) {
			http.Error(w, "session not running", http.StatusConflict)
			return
		}
		// new-session -A attaches (the session exists per the check above).
		cmd = exec.Command("tmux", "new-session", "-A", "-s", tmuxName(session))
	} else {
		cmd = exec.Command(envOr("AGENT_SHELL", "bash"), "-l")
	}
	// Overlay the current toolchain selection so a no-session shell also reflects a
	// Console change without a Stop→Start (sessions get it via toolchainShellPrefix).
	cmd.Env = applyToolchainEnv(append(os.Environ(), "TERM=xterm-256color"))

	ptmx, err := pty.Start(cmd)
	if err != nil {
		http.Error(w, "pty start failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = ptmx.Close() }()

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // upgrader already wrote the error
	}
	defer conn.Close()

	// The PTY-output goroutine and the ping responder below both write to conn, so
	// guard every write — gorilla forbids concurrent WriteMessage on one connection.
	var writeMu sync.Mutex
	writeMsg := func(mt int, data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteMessage(mt, data)
	}

	// PTY -> WS (binary). Closing ptmx on command exit ends this goroutine.
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if werr := writeMsg(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					log.Printf("pty read: %v", err)
				}
				writeMsg(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "pty closed"))
				return
			}
		}
	}()

	// WS -> PTY (text control frames).
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		switch mt {
		case websocket.TextMessage:
			var m ctrlMsg
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			switch m.Type {
			case "input":
				_, _ = ptmx.Write([]byte(m.Data))
			case "resize":
				_ = pty.Setsize(ptmx, &pty.Winsize{Rows: m.Rows, Cols: m.Cols})
			case "ping":
				// App-level heartbeat: echo a pong so the client can tell this socket is
				// alive end-to-end (the CP proxy relays data frames but swallows protocol
				// ping/pong). A missed pong is how the client detects a dead connection.
				_ = writeMsg(websocket.TextMessage, pongMsg)
			}
		case websocket.BinaryMessage:
			_, _ = ptmx.Write(data) // tolerate raw-byte clients too
		}
	}

	// Client gone: stop the PTY-backed process (tmux attach detaches; shell exits).
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}
