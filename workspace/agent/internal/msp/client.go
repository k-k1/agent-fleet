package msp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// Client is one MSP connection: newline-delimited JSON-RPC 2.0 over a `muse serve` child's
// stdio. It is the same protocol-generic skeleton the ACP kinds use (kiro, cursor and
// copilot each carry a copy in their own acp.go), with the three differences MSP's schema
// forces:
//
//   - ids are a string-or-integer union and "each direction owns its own id space", so a
//     server-initiated request's id travels back verbatim as raw JSON and is never parsed;
//   - the server sends must-answer requests (approval/request, userInput/request) whose
//     response is a presentation receipt that changes no state — the decision itself is a
//     separate command;
//   - the same two things ALSO arrive as notifications, and measured, that is the delivery a
//     real host uses. OnNotification is therefore not an optional convenience: a client that
//     answers only the request form leaves the turn parked on approvalPending forever
//     (ADR 0095 decision 13).
//
// OnNotification and OnRequest run on the read goroutine and must never block — record and
// return, then answer through Call or Respond from elsewhere.
type Client struct {
	mu      sync.Mutex
	stdin   io.Writer
	pending map[int64]chan rpcResult
	nextID  int64

	onNotify  func(method string, params json.RawMessage)
	onRequest func(id json.RawMessage, method string, params json.RawMessage)

	closeOnce sync.Once
	closed    chan struct{} // closed when the read loop exits (child gone)
}

// Handler receives everything the server initiates.
type Handler struct {
	// OnNotification is called for each of the 31 notification methods. params is the
	// method's own params object, undecoded.
	OnNotification func(method string, params json.RawMessage)
	// OnRequest is called for a must-answer server request. The handler is responsible for
	// calling Respond with a RequestReceipt; not answering stalls that request forever.
	OnRequest func(id json.RawMessage, method string, params json.RawMessage)
}

type rpcResult struct {
	result json.RawMessage
	err    error
}

// Error is an MSP error response. Code is one of the ErrCode constants; Data carries the
// schema's structured detail, which is where a rejection says which field it rejected.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (e *Error) Error() string {
	if len(e.Data) > 0 {
		return fmt.Sprintf("msp %d: %s: %s", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("msp %d: %s", e.Code, e.Message)
}

// Detail decodes the structured error data. It returns nil when there is none.
func (e *Error) Detail() *ErrorData {
	if len(e.Data) == 0 {
		return nil
	}
	var d ErrorData
	if json.Unmarshal(e.Data, &d) != nil {
		return nil
	}
	return &d
}

// HasCode reports whether err is an MSP error with this code. Use it rather than comparing
// message text: the messages are human-facing and the codes are the contract.
func HasCode(err error, code int) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// Retryable reports whether the schema declares this failure transient. Anything outside the
// schema's own retryable set is a client bug or a settled refusal, and retrying it is how a
// clean rejection turns into a loop.
func Retryable(err error) bool {
	var e *Error
	return errors.As(err, &e) && retryableErrorCodes[e.Code]
}

// Settled reports whether err means "someone already answered this". Both the approval and
// the user-input channels re-deliver their prompts, so answering twice is normal traffic and
// this error is a success, not a failure (measured, ADR 0095 decision 13).
func Settled(err error) bool {
	return HasCode(err, ErrCodeUserInputAlreadySettled) || HasCode(err, ErrCodeApprovalAlreadyResolved)
}

// ErrClosed is returned once the child's stdout has closed; every in-flight call fails with it.
var ErrClosed = errors.New("Muse Code との接続が切れました")

// NewClient starts reading stdout and returns a client writing to stdin. It does not own the
// child process: the caller starts and reaps it.
func NewClient(stdin io.Writer, stdout io.Reader, h Handler) *Client {
	c := &Client{
		stdin:     stdin,
		pending:   map[int64]chan rpcResult{},
		closed:    make(chan struct{}),
		onNotify:  h.OnNotification,
		onRequest: h.OnRequest,
	}
	go c.readLoop(stdout)
	return c
}

// Closed is closed when the connection is gone. Select on it to notice a dead child without
// polling.
func (c *Client) Closed() <-chan struct{} { return c.closed }

func (c *Client) readLoop(stdout io.Reader) {
	defer c.markClosed()
	sc := bufio.NewScanner(stdout)
	// A session/read result or a view page carries a whole transcript on one line, so the
	// stock 64 KiB limit would truncate a legitimate message into a decode failure.
	sc.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
	for sc.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *Error          `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		switch {
		case msg.Method != "" && len(msg.ID) > 0 && string(msg.ID) != "null":
			if c.onRequest != nil {
				c.onRequest(msg.ID, msg.Method, msg.Params)
			}
		case msg.Method != "":
			if c.onNotify != nil {
				c.onNotify(msg.Method, msg.Params)
			}
		default:
			c.deliver(msg.ID, msg.Result, msg.Error)
		}
	}
}

// deliver routes a response to its waiting call. Our own ids are always integers, so an id
// this cannot parse belongs to nobody and is dropped rather than guessed at.
func (c *Client) deliver(rawID, result json.RawMessage, rpcErr *Error) {
	var id int64
	if json.Unmarshal(rawID, &id) != nil {
		return
	}
	c.mu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ch == nil {
		return
	}
	var err error
	if rpcErr != nil {
		err = rpcErr
	}
	ch <- rpcResult{result: result, err: err}
}

func (c *Client) markClosed() {
	c.closeOnce.Do(func() { close(c.closed) })
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, ch := range c.pending {
		delete(c.pending, id)
		ch <- rpcResult{err: ErrClosed}
	}
}

func (c *Client) dead() bool {
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
	if c.dead() {
		return ErrClosed
	}
	_, err = c.stdin.Write(append(b, '\n'))
	return err
}

// Call issues a request and waits for its response. timeout 0 waits forever, which is the
// right posture for a command that fronts a turn: a turn legitimately runs for hours, and a
// dead child closes the connection rather than hanging.
func (c *Client) Call(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan rpcResult, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	if err := c.write(req); err != nil {
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
	case r := <-ch:
		return r.result, r.err
	case <-c.closed:
		// markClosed may already have delivered into ch, so collect it before giving up.
		select {
		case r := <-ch:
			return r.result, r.err
		default:
			return nil, ErrClosed
		}
	case <-timer:
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("Muse Code が応答しません: %s", method)
	}
}

// CallInto is Call with the result decoded into out. out may be nil for a command whose
// acknowledgement carries nothing the caller needs.
func (c *Client) CallInto(method string, params any, timeout time.Duration, out any) error {
	raw, err := c.Call(method, params, timeout)
	if err != nil {
		return err
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Notify sends a notification. `initialized` is the only one a v1 client sends.
func (c *Client) Notify(method string, params any) error {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	return c.write(msg)
}

// Respond answers a server-initiated request, echoing its id verbatim — the id is a
// string-or-integer union owned by the server's own id space, so it is never reinterpreted.
func (c *Client) Respond(id json.RawMessage, result any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}
