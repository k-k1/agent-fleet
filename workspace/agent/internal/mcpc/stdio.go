package mcpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

// stderrCap bounds how much of a stdio server's stderr this client keeps for
// diagnostics — enough for a useful error, not an unbounded sink for a chatty server.
const stderrCap = 4 << 10

// stdioKillGrace is how long close() waits for the child to exit on its own (stdin
// closed, which is exactly the "stdin closed" signal RunStdio's loop already treats as
// a clean shutdown — mcp_stdio.go:191) before it escalates to Kill. A var, not a const,
// so a test can shrink it rather than spend real wall-clock time proving the escalation
// works (stdio_test.go's TestStdioClose_KillsAHangingChild).
var stdioKillGrace = 3 * time.Second

// stdioConn is a persistent stdio JSON-RPC connection: one child process, one reader
// goroutine multiplexing responses by id, notifications routed to a channel.
//
// Unlike mcpreg/probe.go's probeStdio (a single reader for the DURATION OF ONE PROBE,
// released via a `done` channel the moment the probe returns — see its own comment on
// why a per-exchange goroutine is wrong), this reader lives for the connection's whole
// lifetime: there is no "abandoned exchange" here because every call's context governs
// only that call's own wait, never the shared reader.
type stdioConn struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser

	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan rpcMsg
	closed  bool

	notifCh chan string
	doneCh  chan struct{} // closed once the reader loop exits (process gone)

	stderrBuf *limitedBuffer

	closeOnce sync.Once
}

// limitedBuffer caps how many bytes a Write accumulates, dropping the rest — a
// misbehaving server's stderr must never grow without bound for the life of a session.
type limitedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len() < stderrCap {
		room := stderrCap - b.buf.Len()
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// dialStdio spawns the server's command. The process is NOT bound to ctx — ctx here
// only governs the spawn/handshake step; killing the child is close()'s job alone, so
// that a per-call or per-handshake deadline expiring can never take the connection down
// out from under a caller who is still using it (ADR 0093 decision 6: "stdio 子はセッ
// ションと共に死ぬこと" — tied to Manager.Close()/the session context passed to
// NewManager, not to any one request's deadline).
func dialStdio(def mcpreg.ServerDef) (*stdioConn, error) {
	cmd := exec.Command(def.Command, def.Args...)
	env := os.Environ()
	for k, v := range def.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcpc: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcpc: stdout pipe: %w", err)
	}
	errBuf := &limitedBuffer{}
	cmd.Stderr = errBuf
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcpc: start %q: %w", def.Command, err)
	}

	c := &stdioConn{
		cmd:       cmd,
		stdin:     stdin,
		pending:   map[int64]chan rpcMsg{},
		notifCh:   make(chan string, 16),
		doneCh:    make(chan struct{}),
		stderrBuf: errBuf,
	}
	go c.readLoop(stdout)
	return c, nil
}

func (c *stdioConn) readLoop(stdout io.Reader) {
	defer close(c.doneCh)
	rd := bufio.NewReaderSize(stdout, 1<<20)
	for {
		line, err := rd.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var m rpcMsg
			if json.Unmarshal(bytes.TrimSpace(line), &m) == nil {
				c.dispatch(m)
			}
		}
		if err != nil {
			return
		}
	}
}

func (c *stdioConn) dispatch(m rpcMsg) {
	if isRequest(m) && m.Method == "" {
		// A response to one of our own calls.
		var id int64
		if json.Unmarshal(m.ID, &id) != nil {
			return
		}
		c.mu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ch != nil {
			ch <- m
		}
		return
	}
	if m.Method == "" {
		return
	}
	if isRequest(m) {
		// A server-initiated request. This client implements none (no sampling, no
		// roots) — answer politely so the server does not hang waiting, rather than
		// silently dropping it.
		c.writeLine(rpcMsg{JSONRPC: "2.0", ID: m.ID, Error: &rpcErr{Code: errMethodNotFound, Message: "not supported by this client"}})
		return
	}
	select {
	case c.notifCh <- m.Method:
	default:
		// A burst of notifications this client isn't draining fast enough: drop rather
		// than block the single reader goroutine (a stuck reader would also starve
		// every in-flight call's response).
	}
}

func (c *stdioConn) writeLine(m rpcMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(append(b, '\n'))
	return err
}

func (c *stdioConn) call(ctx context.Context, method string, params any, stateless bool) (rpcMsg, error) {
	if stateless {
		params = withMeta(toParamsMap(params))
	}
	id := atomic.AddInt64(&c.nextID, 1)
	ch := make(chan rpcMsg, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return rpcMsg{}, fmt.Errorf("mcpc: connection closed")
	}
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.writeLine(rpcMsg{JSONRPC: "2.0", ID: idJSON(id), Method: method, Params: params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return rpcMsg{}, err
	}

	select {
	case m := <-ch:
		return m, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return rpcMsg{}, ctx.Err()
	case <-c.doneCh:
		return rpcMsg{}, fmt.Errorf("mcpc: server process exited: %s", c.stderrBuf.String())
	}
}

func (c *stdioConn) notify(_ context.Context, method string, params any, stateless bool) error {
	if stateless {
		params = withMeta(toParamsMap(params))
	}
	return c.writeLine(rpcMsg{JSONRPC: "2.0", Method: method, Params: params})
}

func (c *stdioConn) notifications() <-chan string { return c.notifCh }

// close shuts the connection down: stdin closes first (the clean-shutdown signal
// RunStdio's own loop already understands — mcp_stdio.go:191 "stdin closed"), and only
// if the child ignores that within stdioKillGrace does it escalate to Kill. Either way
// it waits for the READER goroutine to observe the process gone (c.doneCh) before
// closing notifCh, and always calls cmd.Wait() — skipping it would leak a zombie.
func (c *stdioConn) close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.mu.Unlock()

		_ = c.stdin.Close()
		select {
		case <-c.doneCh:
		case <-time.After(stdioKillGrace):
			if c.cmd.Process != nil {
				_ = c.cmd.Process.Kill()
			}
			<-c.doneCh
		}
		_ = c.cmd.Wait()
		close(c.notifCh)
	})
	return nil
}

// toParamsMap best-effort narrows params to a map so withMeta can add `_meta` to it.
// Every caller in this package only ever passes map[string]any or nil.
func toParamsMap(params any) map[string]any {
	if params == nil {
		return nil
	}
	if m, ok := params.(map[string]any); ok {
		return m
	}
	return nil
}
