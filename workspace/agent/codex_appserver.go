package main

// Codex app-server lifecycle and event monitor.
//
// The interactive Codex TUI connects to this local app-server over loopback.
// A second, read-only AF connection receives the same thread item lifecycle events,
// which gives us a first-class contextCompaction started/completed signal instead
// of scraping version-dependent terminal text.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
)

const codexAppServerEnv = "AF_CODEX_APP_SERVER_ADDR"
const defaultCodexAppServerAddr = "ws://127.0.0.1:7798"

type codexAppServerMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

type codexAppServerItemNotification struct {
	ThreadID string `json:"threadId"`
	Item     struct {
		Type string `json:"type"`
	} `json:"item"`
}

type codexAppServerThreadNotification struct {
	ThreadID string `json:"threadId"`
}

// startCodexAppServer starts the shared local server and its AF observer. Failure is
// deliberately non-fatal: buildProgram sees no env and launches the traditional
// direct TUI, preserving Codex availability at the cost of live compaction state.
func startCodexAppServer() {
	if os.Getenv("AF_CODEX_APP_SERVER_DISABLE") == "1" {
		_ = os.Unsetenv(codexAppServerEnv)
		return
	}
	addr := os.Getenv(codexAppServerEnv)
	if addr == "" {
		// Custom Unix listeners are supported by Codex, but are denied by the
		// Workspace runtime's current syscall policy. A fixed loopback-only port is
		// private to this container's network namespace and needs no bearer token.
		addr = defaultCodexAppServerAddr
	}

	conn, err := connectCodexAppServer(addr)
	if err != nil {
		if strings.HasPrefix(addr, "unix://") {
			path := strings.TrimPrefix(addr, "unix://")
			_ = os.Remove(path) // stale socket from an unclean Agent/app-server exit
		}
		cmd := exec.Command("codex", "app-server", "--listen", addr)
		if err := cmd.Start(); err != nil {
			_ = os.Unsetenv(codexAppServerEnv)
			log.Printf("codex app-server unavailable; using direct TUI: %v", err)
			return
		}
		go func() {
			if err := cmd.Wait(); err != nil {
				log.Printf("codex app-server exited: %v", err)
			}
			codex.ClearCompacting()
			if os.Getenv(codexAppServerEnv) == addr {
				_ = os.Unsetenv(codexAppServerEnv) // future sessions fall back to direct TUI
			}
		}()
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
			conn, err = connectCodexAppServer(addr)
			if err == nil {
				break
			}
		}
		if err != nil {
			_ = cmd.Process.Kill()
			_ = os.Unsetenv(codexAppServerEnv)
			log.Printf("codex app-server did not become ready; using direct TUI: %v", err)
			return
		}
	}
	if err := os.Setenv(codexAppServerEnv, addr); err != nil {
		_ = conn.Close()
		return
	}
	go monitorCodexAppServer(conn, addr)
}

func connectCodexAppServer(addr string) (*websocket.Conn, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	url := addr
	if strings.HasPrefix(addr, "unix://") {
		path := strings.TrimPrefix(addr, "unix://")
		if path == "" {
			return nil, errors.New("empty app-server unix socket path")
		}
		dialer.NetDialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}
		url = "ws://localhost/"
	}
	conn, resp, err := dialer.Dial(url, http.Header{})
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("app-server websocket: %s: %w", resp.Status, err)
		}
		return nil, err
	}
	init := map[string]any{
		"method": "initialize",
		"id":     1,
		"params": map[string]any{
			"clientInfo": map[string]string{
				"name": "agent_fleet", "title": "Agent Fleet", "version": "1",
			},
			// AF needs lifecycle boundaries, not token/terminal deltas. Suppressing
			// high-volume notifications keeps the observer cheap and does not affect
			// the TUI's separate app-server connection.
			"capabilities": map[string]any{"optOutNotificationMethods": []string{
				"item/agentMessage/delta",
				"item/reasoning/summaryTextDelta",
				"item/reasoning/textDelta",
				"item/commandExecution/outputDelta",
				"item/commandExecution/terminalInteraction",
				"item/fileChange/outputDelta",
				"item/fileChange/patchUpdated",
				"item/mcpToolCall/progress",
				"turn/diff/updated",
				"turn/plan/updated",
			}},
		},
	}
	if err := conn.WriteJSON(init); err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			conn.Close()
			return nil, err
		}
		var msg codexAppServerMessage
		if json.Unmarshal(raw, &msg) != nil || string(msg.ID) != "1" {
			continue
		}
		if len(msg.Error) > 0 && string(msg.Error) != "null" {
			conn.Close()
			return nil, fmt.Errorf("app-server initialize: %s", msg.Error)
		}
		break
	}
	_ = conn.SetReadDeadline(time.Time{})
	if err := conn.WriteJSON(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func monitorCodexAppServer(conn *websocket.Conn, addr string) {
	for {
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				_ = conn.Close()
				codex.ClearCompacting()
				log.Printf("codex app-server monitor disconnected: %v", err)
				break
			}
			handleCodexAppServerEvent(raw)
		}
		// The app-server remains authoritative for the TUI even if only AF's
		// observer socket dropped. Reconnect so later compactions are not silently
		// missed; initialization subscribes this connection to subsequently loaded
		// threads again.
		for {
			time.Sleep(time.Second)
			var err error
			conn, err = connectCodexAppServer(addr)
			if err == nil {
				break
			}
		}
	}
}

func handleCodexAppServerEvent(raw []byte) {
	var msg codexAppServerMessage
	if json.Unmarshal(raw, &msg) != nil {
		return
	}
	switch msg.Method {
	case "item/started", "item/completed":
		var p codexAppServerItemNotification
		if json.Unmarshal(msg.Params, &p) != nil || p.ThreadID == "" || p.Item.Type != "contextCompaction" {
			return
		}
		codex.SetCompacting(p.ThreadID, msg.Method == "item/started")
	case "turn/completed":
		// A failed/aborted compaction may end its turn without item/completed.
		var p codexAppServerThreadNotification
		if json.Unmarshal(msg.Params, &p) == nil && p.ThreadID != "" {
			codex.SetCompacting(p.ThreadID, false)
		}
	}
}
