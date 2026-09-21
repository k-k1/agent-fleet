// Package msptest is an in-process MSP host for tests.
//
// It exists because the credential-free route the vendor advertises does not reach this
// client. `muse exec --provider echo` runs a whole session with no account, but `--provider`
// is an exec startup flag and `muse serve` has no equivalent: measured on 1.3.0-R3401.1,
// `session/start` accepts `providerId: "echo"` and records it on the session, and the turn
// still fails `turn/completed.error.kind = "authRequired"`. ADR 0095 decision 2 makes `serve`
// the only process AF runs, so every turn-shaped test either spends a member's subscription
// quota or talks to this.
//
// It is deliberately not a simulator of Muse: it replays scripted server traffic and records
// what the client sent, so a test states the wire sequence it is asserting about.
package msptest

import (
	"bufio"
	"encoding/json"
	"io"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
)

// Message is one line the client sent.
type Message struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
}

// Host is a fake MSP server wired to a Client over in-memory pipes.
type Host struct {
	t *testing.T

	toClient   *io.PipeWriter
	fromClient *io.PipeReader

	mu       sync.Mutex
	received []Message
	handlers map[string]func(Message) (any, *msp.Error)

	gotMsg chan struct{}
	done   chan struct{}
}

// New starts a host and returns it with a client already reading from it. The client's
// Handler is the caller's, so a test sees the notifications the host emits.
func New(t *testing.T, h msp.Handler) (*Host, *msp.Client) {
	t.Helper()
	clientReads, hostWrites := io.Pipe()
	hostReads, clientWrites := io.Pipe()
	host := &Host{
		t:          t,
		toClient:   hostWrites,
		fromClient: hostReads,
		handlers:   map[string]func(Message) (any, *msp.Error){},
		gotMsg:     make(chan struct{}, 64),
		done:       make(chan struct{}),
	}
	client := msp.NewClient(clientWrites, clientReads, h)
	go host.readLoop()
	t.Cleanup(func() {
		host.Close()
		<-host.done
	})
	return host, client
}

// Handle registers the reply for a method. Returning a non-nil *msp.Error answers with an
// error response instead of a result, which is how a test exercises the idempotency paths
// (userInputAlreadySettled and friends).
func (h *Host) Handle(method string, fn func(Message) (any, *msp.Error)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handlers[method] = fn
}

// Notify sends a notification to the client.
func (h *Host) Notify(method string, params any) {
	h.t.Helper()
	h.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// Request sends a server-initiated request with the given raw id. The id is written verbatim,
// so a test can hand over a string id and assert the client echoes it back unchanged.
func (h *Host) Request(rawID string, method string, params any) {
	h.t.Helper()
	h.write(json.RawMessage(`{"jsonrpc":"2.0","id":` + rawID + `,"method":"` + method + `","params":` + mustJSON(params) + `}`))
}

// Close drops the connection from the host side, which is what a dead `muse serve` child
// looks like to the client.
func (h *Host) Close() {
	h.toClient.Close()
	h.fromClient.Close()
}

// Received returns every message the client has sent so far.
func (h *Host) Received() []Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Message(nil), h.received...)
}

// WaitFor blocks until the client has sent a message matching pred, and returns it. It fails
// the test rather than hanging when the connection dies first.
func (h *Host) WaitFor(pred func(Message) bool) Message {
	h.t.Helper()
	for {
		h.mu.Lock()
		for _, m := range h.received {
			if pred(m) {
				h.mu.Unlock()
				return m
			}
		}
		h.mu.Unlock()
		select {
		case <-h.gotMsg:
		case <-h.done:
			h.t.Fatalf("connection closed before the expected message arrived; got %d messages", len(h.Received()))
		}
	}
}

// WaitForMethod is WaitFor for the common case.
func (h *Host) WaitForMethod(method string) Message {
	h.t.Helper()
	return h.WaitFor(func(m Message) bool { return m.Method == method })
}

func (h *Host) readLoop() {
	defer close(h.done)
	sc := bufio.NewScanner(h.fromClient)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var m Message
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		h.mu.Lock()
		h.received = append(h.received, m)
		fn := h.handlers[m.Method]
		h.mu.Unlock()
		select {
		case h.gotMsg <- struct{}{}:
		default:
		}
		// Only a request (method plus id) gets a reply; notifications and the client's own
		// responses to server requests do not.
		if fn == nil || m.Method == "" || len(m.ID) == 0 {
			continue
		}
		result, rpcErr := fn(m)
		if rpcErr != nil {
			h.write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": rpcErr})
			continue
		}
		h.write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
	}
}

func (h *Host) write(v any) {
	var b []byte
	if raw, ok := v.(json.RawMessage); ok {
		b = raw
	} else {
		b = []byte(mustJSON(v))
	}
	// A closed pipe means the test finished; that is not a failure to report.
	h.toClient.Write(append(b, '\n'))
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}
