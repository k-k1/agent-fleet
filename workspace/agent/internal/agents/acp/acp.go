// Package acp is what the ACP drivers (copilot, cursor, kiro) share: the JSON-RPC client for
// the runtime child, the in-memory transcript a managed session builds from session/update, and
// the tool-progress probe. Each kind passes its differences in as parameters; the dispatch of
// session/update and of the kind's own notifications stays in the kind's driver.go.
package acp

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"
)

// Config is what differs between the kinds' clients.
type Config struct {
	// Runtime names the runtime in the user-visible connection errors ("kiro runtime …").
	Runtime string
	// ErrorData keeps the JSON-RPC error's `data` member and folds it into Error(). kiro packs
	// the human-useful detail there ("Session is active in another process (PID …)") and its
	// isLockBusy needs it; copilot and cursor never surfaced it, so their error text stays the
	// bare message.
	ErrorData bool
}

type rpcResult struct {
	result json.RawMessage
	err    error
}

// RPCError is the JSON-RPC error object.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (e *RPCError) Error() string {
	if len(e.Data) > 0 {
		return e.Message + ": " + string(e.Data)
	}
	return e.Message
}

// Client is the JSON-RPC 2.0 client (newline-delimited, over stdio) for an ACP child process
// (`copilot --acp`: docs/log/36; `cursor-agent acp`: docs/log/40; `kiro-cli acp`: docs/log/43
// Track A2).
//   - Call: allocate an id, write, wait for the response (timeout 0 = forever, for
//     session/prompt)
//   - NotifyPeer: notifications (session/cancel)
//   - Respond: answers to server-initiated requests (session/request_permission,
//     cursor/ask_question)
//
// readLoop routes responses, notifications and server-initiated requests. The callbacks are
// called synchronously on the readLoop goroutine, so they must never block: the permission
// handler records an Interaction and returns immediately, answering later through Respond(),
// and OnNotify only appends to the transcript buffer.
//
// NewClient does not read; Start does, with the callbacks fixed for the client's lifetime. A
// line read before the callbacks were set would be dropped, and the late assignment would race
// with readLoop.
//
// kiro also emits its own notifications in the `_kiro.dev/*` namespace. They arrive without an
// id, so OnNotify receives them and the driver keeps the ones it needs and drops the rest.
type Client struct {
	cfg Config

	mu      sync.Mutex
	stdin   io.Writer
	stdout  io.Reader
	pending map[int64]chan rpcResult
	nextID  int64

	cb Callbacks

	errClosed error

	startOnce sync.Once
	closeOnce sync.Once
	closed    chan struct{} // closed when the readLoop exits (child gone)
}

// Callbacks are the handlers readLoop invokes. Either may be nil (the line is dropped). They
// run on the readLoop goroutine and must never block.
type Callbacks struct {
	// OnRequest receives a server-initiated request that must be answered through Respond;
	// c is the receiving client, so a handler need not capture it.
	OnRequest func(c *Client, id json.RawMessage, method string, params json.RawMessage)
	OnNotify  func(method string, params json.RawMessage)
}

// NewClient builds a client without reading the child's stdout; call Start to begin.
func NewClient(cfg Config, stdin io.Writer, stdout io.Reader) *Client {
	c := &Client{
		cfg:       cfg,
		stdin:     stdin,
		pending:   map[int64]chan rpcResult{},
		errClosed: errors.New(cfg.Runtime + " runtime との接続が切れました"),
		closed:    make(chan struct{}),
	}
	c.stdout = stdout
	return c
}

// Start begins reading the child's stdout and dispatching to cb. Only the first call has an
// effect. Until it runs nothing is read, so no line can reach a handler that is not yet set.
func (c *Client) Start(cb Callbacks) {
	c.startOnce.Do(func() {
		c.cb = cb
		go c.readLoop(c.stdout)
	})
}

// readLoop dispatches incoming lines until the child's stdout closes. cursor's
// available_commands_update and kiro's `_kiro.dev/commands/available` (the skill list) arrive
// as a single large line, so the buffer is generous.
func (c *Client) readLoop(stdout io.Reader) {
	defer c.MarkClosed()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
	for sc.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *RPCError       `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		switch {
		case msg.Method != "" && len(msg.ID) > 0:
			// server-initiated request (session/request_permission and the like)
			if c.cb.OnRequest != nil {
				c.cb.OnRequest(c, msg.ID, msg.Method, msg.Params)
			}
		case msg.Method != "":
			// notification (session/update, _kiro.dev/*, …)
			if c.cb.OnNotify != nil {
				c.cb.OnNotify(msg.Method, msg.Params)
			}
		default:
			var id int64
			if json.Unmarshal(msg.ID, &id) != nil {
				continue
			}
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				var err error
				if msg.Error != nil {
					if !c.cfg.ErrorData {
						msg.Error.Data = nil
					}
					err = msg.Error
				}
				ch <- rpcResult{result: msg.Result, err: err}
			}
		}
	}
}

// MarkClosed fails every in-flight call and marks the client dead. readLoop calls it when stdout
// closes, and the driver's watch once the child is reaped.
func (c *Client) MarkClosed() {
	c.closeOnce.Do(func() { close(c.closed) })
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, ch := range c.pending {
		delete(c.pending, id)
		ch <- rpcResult{err: c.errClosed}
	}
}

func (c *Client) Dead() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func (c *Client) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Dead() {
		return c.errClosed
	}
	_, err = c.stdin.Write(append(b, '\n'))
	return err
}

// Call issues a request and waits for its response. timeout 0 = wait forever
// (a turn legitimately runs for minutes/hours — interrupt or child death unblocks it).
func (c *Client) Call(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	return c.CallWritten(method, params, timeout, nil)
}

// CallWritten is Call that runs written once the request is on the child's stdin, before the
// wait. session/prompt answers only when the turn ends, so this is the only point at which the
// driver can say the runtime has the input (ADR 0105 decision 3). written is not run when the
// write fails. It runs on the caller's goroutine, ahead of the wait, so it must not block.
func (c *Client) CallWritten(method string, params any, timeout time.Duration, written func()) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan rpcResult, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	if err := c.write(req); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	if written != nil {
		written()
	}
	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}
	select {
	case r := <-ch:
		return r.result, r.err
	case <-c.closed:
		// markClosed may already have delivered to pending, so try to collect it.
		select {
		case r := <-ch:
			return r.result, r.err
		default:
			return nil, c.errClosed
		}
	case <-timer:
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, errors.New(c.cfg.Runtime + " runtime が応答しません: " + method)
	}
}

func (c *Client) NotifyPeer(method string, params any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// Respond answers a server-initiated request by its raw id.
func (c *Client) Respond(id json.RawMessage, result any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

// RespondError answers a server-initiated request with a JSON-RPC error. An unanswered request
// wedges the turn, so the drivers answer the ones they do not handle with -32601.
func (c *Client) RespondError(id json.RawMessage, code int, message string) error {
	return c.write(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": message},
	})
}
