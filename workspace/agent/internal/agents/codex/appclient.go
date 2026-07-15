package codex

// appClient は codex app-server への managed writer 接続（WS JSON-RPC）。
// supervisor（serve.go）が generation 単位で 1 本張り、全 managed handle が共有する。
// 役割: リクエスト/レスポンス相関・通知の handle への配送・server-initiated
// request（質問/承認）の受け口。読み手は readLoop の 1 goroutine、書き手は wmu で
// 直列化する（gorilla/websocket の並行制約）。
//
// initialize は experimentalApi capability を宣言する — `thread/settings/update`
// が要求するため（§12.1-4 実測: 無いと -32600）。高頻度の delta 系通知は観測接続
// （package main の codexObserver）と同じ理由で opt out する。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type rpcMsg struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

type appClient struct {
	conn *websocket.Conn
	wmu  sync.Mutex // serializes writes

	mu      sync.Mutex
	nextID  int
	pending map[int]chan rpcMsg
	closed  bool

	// onClosed fires once when the read loop exits (supervisor sets it before
	// starting the loop).
	onClosed func()
}

// dialAppServer opens the websocket (ws:// or unix://, mirroring package main's
// observer dialing).
func dialAppServer(addr string) (*websocket.Conn, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
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
	return conn, nil
}

// newAppClient dials and performs the initialize handshake synchronously (the
// read loop is started by the supervisor afterwards).
func newAppClient(addr string) (*appClient, error) {
	conn, err := dialAppServer(addr)
	if err != nil {
		return nil, err
	}
	init := map[string]any{
		"method": "initialize",
		"id":     1,
		"params": map[string]any{
			"clientInfo": map[string]string{
				"name": "agent_fleet_driver", "title": "Agent Fleet Driver", "version": "1",
			},
			"capabilities": map[string]any{
				// thread/settings/update（モデル/effort/モードの動的変更）は
				// experimentalApi が initialize で必須（§12.1-4 実測）。
				"experimentalApi": true,
				// token/terminal delta はミラー転写（rollout 読み）に不要 — 観測接続と
				// 同じ opt out で writer を安く保つ。
				"optOutNotificationMethods": []string{
					"item/agentMessage/delta",
					"item/plan/delta",
					"item/reasoning/summaryPartAdded",
					"item/reasoning/summaryTextDelta",
					"item/reasoning/textDelta",
					"item/commandExecution/outputDelta",
					"item/commandExecution/terminalInteraction",
					"item/fileChange/outputDelta",
					"item/fileChange/patchUpdated",
					"item/mcpToolCall/progress",
					"turn/diff/updated",
					"turn/plan/updated",
					"thread/tokenUsage/updated",
				},
			},
		},
	}
	if err := conn.WriteJSON(init); err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			conn.Close()
			return nil, err
		}
		var msg rpcMsg
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
	// id 1 は initialize が使用済み — 十分離れた所から採番する。
	return &appClient{conn: conn, nextID: 100, pending: map[int]chan rpcMsg{}}, nil
}

func (c *appClient) alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed
}

func (c *appClient) close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	_ = c.conn.Close()
}

// writeJSON serializes concurrent writers (turn goroutines, respond, interrupt).
func (c *appClient) writeJSON(v any) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.conn.WriteJSON(v)
}

// call sends a request and waits for its response. timeout 0 = no deadline
// （turn/start も応答は即返る — turn の完走は notification 側 — ので通常は短い）。
func (c *appClient) call(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("writer connection is closed")
	}
	c.nextID++
	id := c.nextID
	ch := make(chan rpcMsg, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.writeJSON(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return nil, errors.New("writer connection lost")
		}
		if len(m.Error) > 0 && string(m.Error) != "null" {
			return nil, fmt.Errorf("%s: %s", method, m.Error)
		}
		return m.Result, nil
	case <-timer:
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("%s: timeout", method)
	}
}

// respond answers a server-initiated request (question / approval) by raw id.
func (c *appClient) respond(id json.RawMessage, result any) error {
	return c.writeJSON(map[string]any{"id": id, "result": result})
}

// readLoop pumps the connection until it breaks, dispatching responses, server
// requests and notifications. On exit every pending call unblocks with an error
// and onClosed fires exactly once (supervisor: handles→unknown→reconcile).
func (c *appClient) readLoop() {
	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
		var msg rpcMsg
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		switch {
		case msg.Method == "" && len(msg.ID) > 0: // response
			var id int
			if json.Unmarshal(msg.ID, &id) != nil {
				continue
			}
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
		case msg.Method != "" && len(msg.ID) > 0: // server-initiated request
			c.handleServerRequest(msg)
		default: // notification
			dispatchNotification(msg)
		}
	}
	c.mu.Lock()
	c.closed = true
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	onClosed := c.onClosed
	c.onClosed = nil
	c.mu.Unlock()
	if onClosed != nil {
		onClosed()
	}
}

// handleServerRequest routes server→client requests. 質問（item/tool/
// requestUserInput）が本命（§5）。承認系は approvalPolicy=never＋dangerFullAccess
// で運転しているため来ない想定だが、ユーザー config が承認を足しても managed
// セッションが黙って固まらないよう自動承認で受ける（opencode の permission.asked
// 自動 allow と同じ保険、§5 — コンテナがサンドボックス）。
func (c *appClient) handleServerRequest(msg rpcMsg) {
	switch msg.Method {
	case "item/tool/requestUserInput":
		var p userInputRequest
		if json.Unmarshal(msg.Params, &p) != nil || p.ThreadID == "" {
			return
		}
		deliverUserInputRequest(c, msg.ID, p)
	case "execCommandApproval", "applyPatchApproval": // legacy v1 response enum
		_ = c.respond(msg.ID, map[string]any{"decision": "approved"})
		log.Printf("codex managed: auto-approved %s", msg.Method)
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		// v2 の decision enum は legacy の approved でなく accept。
		_ = c.respond(msg.ID, map[string]any{"decision": "accept"})
		log.Printf("codex managed: auto-approved %s", msg.Method)
	case "item/permissions/requestApproval":
		// RequestPermissionProfile と GrantedPermissionProfile は同じ
		// fileSystem/network shape。要求された権限だけを turn scope で返す。
		var p struct {
			Permissions json.RawMessage `json:"permissions"`
		}
		if json.Unmarshal(msg.Params, &p) != nil || len(p.Permissions) == 0 {
			log.Printf("codex managed: invalid permissions request (id %s)", msg.ID)
			return
		}
		_ = c.respond(msg.ID, map[string]any{"permissions": p.Permissions, "scope": "turn"})
		log.Printf("codex managed: auto-approved %s", msg.Method)
	default:
		// 応答できない要求は黙って捨てない — 相関付きで観測できるようログに残す。
		log.Printf("codex managed: unhandled server request %s (id %s)", msg.Method, msg.ID)
	}
}
