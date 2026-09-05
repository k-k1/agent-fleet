package kiro

// acpClient is the JSON-RPC 2.0 client (newline-delimited, over stdio) for the `kiro-cli acp`
// child process (docs/log/43 Track A2, measured on 2.14.1). It is the same protocol-generic
// skeleton as cursor's and copilot's acp.go; the kiro-specific parts live in driver.go (the
// session/update dispatch in onNotify and the handling of `_kiro.dev/metadata` notifications).
//   - call: allocate an id, write, wait for the response (timeout 0 = forever, for
//     session/prompt)
//   - notifyPeer: notifications (session/cancel)
//   - respond: answers to server-initiated requests (session/request_permission)
//
// readLoop routes responses, notifications and server-initiated requests. onRequest and
// onNotify are called synchronously on the readLoop goroutine, so they must never block:
// the permission handler records an Interaction and returns immediately, answering later
// through respond(), and onNotify only appends to the transcript buffer.
//
// Besides the standard ACP session/update, kiro emits its own notifications in the
// `_kiro.dev/*` namespace (metadata / subagent/list_update / commands/available /
// session/update=retry_warning). They all arrive without an id, so onNotify receives them and
// the driver keeps the ones it needs and silently drops the rest (no response is expected).

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"
)

type rpcResult struct {
	result json.RawMessage
	err    error
}

// rpcError is the JSON-RPC error object. kiro packs the human-useful detail into the
// `data` field (e.g. "Session is active in another process (PID …)"), which isLockBusy needs to
// spot a lock conflict, so Error() folds it into the message.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (e *rpcError) Error() string {
	if len(e.Data) > 0 {
		return e.Message + ": " + string(e.Data)
	}
	return e.Message
}

type acpClient struct {
	mu      sync.Mutex
	stdin   io.Writer
	pending map[int64]chan rpcResult
	nextID  int64

	onNotify  func(method string, params json.RawMessage)
	onRequest func(id json.RawMessage, method string, params json.RawMessage)

	closeOnce sync.Once
	closed    chan struct{} // closed when the readLoop exits (child gone)
}

func newACPClient(stdin io.Writer, stdout io.Reader) *acpClient {
	c := &acpClient{
		stdin:   stdin,
		pending: map[int64]chan rpcResult{},
		closed:  make(chan struct{}),
	}
	go c.readLoop(stdout)
	return c
}

var errClientClosed = errors.New("kiro runtime との接続が切れました")

// readLoop dispatches incoming lines until the child's stdout closes. kiro streams a large
// `_kiro.dev/commands/available` (the skill/command list) as a single line, so the buffer is
// generous, same as cursor's.
func (c *acpClient) readLoop(stdout io.Reader) {
	defer c.markClosed()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
	for sc.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		switch {
		case msg.Method != "" && len(msg.ID) > 0:
			// server-initiated request (session/request_permission and the like)
			if c.onRequest != nil {
				c.onRequest(msg.ID, msg.Method, msg.Params)
			}
		case msg.Method != "":
			// notification (session/update, _kiro.dev/*, …)
			if c.onNotify != nil {
				c.onNotify(msg.Method, msg.Params)
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
					err = msg.Error
				}
				ch <- rpcResult{result: msg.Result, err: err}
			}
		}
	}
}

// markClosed fails every in-flight call and marks the client dead.
func (c *acpClient) markClosed() {
	c.closeOnce.Do(func() { close(c.closed) })
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, ch := range c.pending {
		delete(c.pending, id)
		ch <- rpcResult{err: errClientClosed}
	}
}

func (c *acpClient) dead() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func (c *acpClient) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead() {
		return errClientClosed
	}
	_, err = c.stdin.Write(append(b, '\n'))
	return err
}

// call issues a request and waits for its response. timeout 0 = wait forever
// (a turn legitimately runs for minutes/hours — interrupt or child death unblocks it).
func (c *acpClient) call(method string, params any, timeout time.Duration) (json.RawMessage, error) {
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
			return nil, errClientClosed
		}
	case <-timer:
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, errors.New("kiro runtime が応答しません: " + method)
	}
}

func (c *acpClient) notifyPeer(method string, params any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// respond answers a server-initiated request by its raw id.
func (c *acpClient) respond(id json.RawMessage, result any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}
