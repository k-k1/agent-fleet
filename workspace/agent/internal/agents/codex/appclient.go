package codex

// appClient is the managed writer connection to codex app-server (WS JSON-RPC). The supervisor
// (serve.go) opens one per generation and every managed handle shares it. Its jobs: correlating
// requests with responses, delivering notifications to handles, and receiving server-initiated
// requests (questions and approvals). Reading happens on the single readLoop goroutine; writes
// are serialized through wmu (gorilla/websocket's concurrency constraint).
//
// initialize declares the experimentalApi capability because `thread/settings/update` requires
// it (measured, §12.1-4: -32600 without it). High-frequency delta notifications are opted out
// of for the same reason as the observation connection (codexObserver in package main).

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

// rpcError is a decoded JSONRPCErrorError ({code, message, data}) — a rejected turn/start
// (e.g. a usage-limit rejection that never creates a Turn to fail) reports this way
// instead of a turn/completed notification. Kept as a typed error (rather than folded
// into a bare string) so callers can recover the message/data via errors.As without
// re-parsing call()'s flattened text (errors.go's codexErrorFromRPC).
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return e.Message }

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
				// thread/settings/update (dynamic model / effort / mode changes) requires
				// experimentalApi at initialize (measured, §12.1-4).
				"experimentalApi": true,
				// token/terminal deltas are not needed for mirror transcription, which reads
				// the rollout; the same opt out as the observation connection keeps the
				// writer cheap.
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
	// id 1 is already taken by initialize; number from far enough away.
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

// call sends a request and waits for its response. timeout 0 = no deadline (even turn/start
// answers immediately, since a turn's completion arrives as a notification, so it is normally
// short).
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
			var re rpcError
			if json.Unmarshal(m.Error, &re) == nil && re.Message != "" {
				return nil, &re
			}
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

// handleServerRequest routes server->client requests. Questions (item/tool/requestUserInput)
// are the real case (§5). Approvals are not expected, because we run with approvalPolicy=never
// plus dangerFullAccess, but they are auto-approved so that a managed session does not freeze
// silently when a user config adds approvals back (the same insurance as opencode's automatic
// allow of permission.asked, §5 - the container is the sandbox).
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
		// the v2 decision enum is accept, not legacy's approved.
		_ = c.respond(msg.ID, map[string]any{"decision": "accept"})
		log.Printf("codex managed: auto-approved %s", msg.Method)
	case "item/permissions/requestApproval":
		// RequestPermissionProfile and GrantedPermissionProfile share one fileSystem/network
		// shape. Return only the permissions that were asked for, scoped to the turn.
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
		// A request we cannot answer is never dropped silently - log it so it stays observable
		// together with its correlation id.
		log.Printf("codex managed: unhandled server request %s (id %s)", msg.Method, msg.ID)
	}
}
