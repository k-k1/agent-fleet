package acp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// startPeer runs a client against an in-process peer that answers each request with reply(id).
func startPeer(t *testing.T, cfg Config, reply func(id int64) string) (*Client, func()) {
	t.Helper()
	cIn, toPeer := io.Pipe()
	fromPeer, cOut := io.Pipe()
	cl := NewClient(cfg, toPeer, fromPeer)
	cl.Start(Callbacks{})
	go func() {
		sc := bufio.NewScanner(cIn)
		for sc.Scan() {
			var req struct {
				ID int64 `json:"id"`
			}
			if json.Unmarshal(sc.Bytes(), &req) != nil {
				continue
			}
			if _, err := io.WriteString(cOut, reply(req.ID)+"\n"); err != nil {
				return
			}
		}
	}()
	return cl, func() { _ = cOut.Close(); _ = cIn.Close() }
}

// The error's data member reaches Error() only for a kind that asks for it: kiro's isLockBusy
// reads the lock holder from it, and copilot's and cursor's error text never carried it.
func TestCallErrorData(t *testing.T) {
	reply := func(id int64) string {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id,
			"error": map[string]any{"code": -32603, "message": "Internal error", "data": "Session is active in another process"}})
		return string(b)
	}
	for _, tc := range []struct {
		errorData bool
		want      string
	}{
		{false, "Internal error"},
		{true, `Internal error: "Session is active in another process"`},
	} {
		cl, stop := startPeer(t, Config{Runtime: "x", ErrorData: tc.errorData}, reply)
		_, err := cl.Call("m", map[string]any{}, 5*time.Second)
		stop()
		var re *RPCError
		if !errors.As(err, &re) || re.Code != -32603 {
			t.Fatalf("ErrorData=%v: err=%v, want an RPCError -32603", tc.errorData, err)
		}
		if err.Error() != tc.want {
			t.Errorf("ErrorData=%v: Error()=%q want %q", tc.errorData, err.Error(), tc.want)
		}
	}
}

// The connection errors name the kind's runtime: they reach the user as they are.
func TestErrorsNameTheRuntime(t *testing.T) {
	cl, stop := startPeer(t, Config{Runtime: "cursor"}, func(int64) string { return "" })
	if _, err := cl.Call("session/new", nil, 50*time.Millisecond); err == nil || err.Error() != "cursor runtime が応答しません: session/new" {
		t.Fatalf("timeout err=%v", err)
	}
	stop()
	for deadline := time.Now().Add(5 * time.Second); !cl.Dead(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("client still alive after its peer closed stdout")
		}
	}
	if _, err := cl.Call("m", nil, time.Second); err == nil || err.Error() != "cursor runtime との接続が切れました" {
		t.Fatalf("closed err=%v", err)
	}
}

// A server request and a notification already waiting on stdout when the client starts are each
// delivered exactly once, and the request is answered. Before Start existed the constructor began
// reading at once, so these lines hit nil handlers (dropped) and the late assignment raced with
// readLoop (go test -race).
func TestStartDeliversBufferedLines(t *testing.T) {
	stdout := io.MultiReader(strings.NewReader(
		`{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"a":1}}`+"\n"+
			`{"jsonrpc":"2.0","method":"session/update","params":{"b":2}}`+"\n"),
		blockForever{})
	sent := &lockedWriter{}
	cl := NewClient(Config{Runtime: "x"}, sent, stdout)

	reqs := make(chan string, 4)
	notes := make(chan string, 4)
	cl.Start(Callbacks{
		OnRequest: func(c *Client, id json.RawMessage, method string, params json.RawMessage) {
			reqs <- method + " " + string(id)
			_ = c.Respond(id, map[string]any{"ok": true})
		},
		OnNotify: func(method string, params json.RawMessage) { notes <- method },
	})
	if got := recvOrFail(t, reqs); got != "session/request_permission 7" {
		t.Errorf("request = %q", got)
	}
	if got := recvOrFail(t, notes); got != "session/update" {
		t.Errorf("notification = %q", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(sent.String(), `"id":7`) {
		if time.Now().After(deadline) {
			t.Fatalf("request was not answered; stdin got %q", sent.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case x := <-reqs:
		t.Errorf("request delivered twice: %q", x)
	case x := <-notes:
		t.Errorf("notification delivered twice: %q", x)
	case <-time.After(50 * time.Millisecond):
	}
}

func recvOrFail(t *testing.T, ch chan string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("callback did not fire")
		return ""
	}
}

// blockForever is a stdout that stays open after the buffered lines.
type blockForever struct{}

func (blockForever) Read([]byte) (int, error) { select {} }

// lockedWriter lets the test read what the client wrote while readLoop is still writing.
type lockedWriter struct {
	mu sync.Mutex
	w  bytes.Buffer
}

func (l *lockedWriter) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.String()
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
