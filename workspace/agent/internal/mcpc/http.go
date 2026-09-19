package mcpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

// maxHTTPBody bounds a response body the same way mcpreg/probe.go does (a runaway
// server must not be read into memory without limit).
const maxHTTPBody = 4 << 20

// httpConn is a Streamable HTTP JSON-RPC connection (MCP transport spec): every
// call/notify is its own POST, plus a best-effort GET that opens an SSE stream for
// server-initiated messages (notifications/tools/list_changed chief among them).
//
// Unlike mcpreg/probe.go's probeHTTP (one POST per era-detection step, then done),
// this keeps the legacy era's Mcp-Session-Id for the connection's whole life and keeps
// the GET stream open until close().
type httpConn struct {
	url     string
	headers map[string]string
	client  *http.Client

	nextID int64

	mu      sync.Mutex
	session string // legacy era only

	notifCh chan string

	streamCancel context.CancelFunc
	streamDone   chan struct{}

	closed atomic.Bool
}

func dialHTTP(def mcpreg.ServerDef) (*httpConn, error) {
	if strings.TrimSpace(def.URL) == "" {
		return nil, fmt.Errorf("mcpc: http server %q has no URL", def.Name)
	}
	return &httpConn{
		url:     def.URL,
		headers: def.Headers,
		client:  &http.Client{},
		notifCh: make(chan string, 16),
	}, nil
}

func (h *httpConn) call(ctx context.Context, method string, params any, stateless bool) (rpcMsg, error) {
	if stateless {
		params = withMeta(toParamsMap(params))
	}
	id := atomic.AddInt64(&h.nextID, 1)
	return h.post(ctx, rpcMsg{JSONRPC: "2.0", ID: idJSON(id), Method: method, Params: params}, stateless, method)
}

func (h *httpConn) notify(ctx context.Context, method string, params any, stateless bool) error {
	if stateless {
		params = withMeta(toParamsMap(params))
	}
	_, err := h.post(ctx, rpcMsg{JSONRPC: "2.0", Method: method, Params: params}, stateless, method)
	return err
}

func (h *httpConn) post(ctx context.Context, m rpcMsg, stateless bool, method string) (rpcMsg, error) {
	body, err := json.Marshal(m)
	if err != nil {
		return rpcMsg{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		return rpcMsg{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if stateless {
		// The transport mirrors these body fields into headers (mcpreg/probe.go's
		// probeHTTP comment explains why: intermediaries can route without parsing).
		req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
		req.Header.Set("Mcp-Method", method)
	} else {
		req.Header.Set("MCP-Protocol-Version", ProtocolVersionLegacy)
		h.mu.Lock()
		sid := h.session
		h.mu.Unlock()
		if sid != "" {
			req.Header.Set("Mcp-Session-Id", sid)
		}
	}
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return rpcMsg{}, err
	}
	defer resp.Body.Close()

	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" && !stateless {
		h.mu.Lock()
		h.session = sid
		h.mu.Unlock()
	}

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody))
	if resp.StatusCode == http.StatusAccepted || len(bytes.TrimSpace(respBody)) == 0 {
		return rpcMsg{}, nil // notification ack, or empty 200 (call() callers ignore the zero value)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return rpcMsg{}, fmt.Errorf("mcpc: HTTP %d: %s", resp.StatusCode, tail(respBody))
	}
	return decodeHTTPPayload(resp.Header.Get("Content-Type"), respBody)
}

// decodeHTTPPayload handles both shapes a Streamable HTTP server may answer with,
// mirroring mcpreg/probe.go's function of the same name.
func decodeHTTPPayload(contentType string, body []byte) (rpcMsg, error) {
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		sc := bufio.NewScanner(bytes.NewReader(body))
		sc.Buffer(make([]byte, 0, 64<<10), maxHTTPBody)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var m rpcMsg
			if json.Unmarshal([]byte(strings.TrimSpace(line[len("data:"):])), &m) == nil {
				return m, nil
			}
		}
		return rpcMsg{}, fmt.Errorf("mcpc: SSE response carried no JSON-RPC message")
	}
	var m rpcMsg
	if err := json.Unmarshal(bytes.TrimSpace(body), &m); err != nil {
		return rpcMsg{}, fmt.Errorf("mcpc: cannot decode response: %w", err)
	}
	return m, nil
}

func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	const max = 600
	if len(s) <= max {
		return s
	}
	return "…" + s[len(s)-max:]
}

// startStream opens the best-effort GET SSE stream the Streamable HTTP transport spec
// allows a client to hold for server-initiated messages. Not every server implements
// it — a non-2xx or an immediately closed body just means this connection never
// receives notifications/tools/list_changed and Server.watchNotifications never fires,
// which is a documented degrade-to-poll-free-and-stale-until-restart limitation, not a
// connection failure (initialize/tools/list/tools/call all still work over POST).
func (h *httpConn) startStream(stateless bool) {
	ctx, cancel := context.WithCancel(context.Background())
	h.streamCancel = cancel
	h.streamDone = make(chan struct{})
	go func() {
		defer close(h.streamDone)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.url, nil)
		if err != nil {
			return
		}
		req.Header.Set("Accept", "text/event-stream")
		if stateless {
			req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
		} else {
			req.Header.Set("MCP-Protocol-Version", ProtocolVersionLegacy)
			h.mu.Lock()
			sid := h.session
			h.mu.Unlock()
			if sid != "" {
				req.Header.Set("Mcp-Session-Id", sid)
			}
		}
		for k, v := range h.headers {
			req.Header.Set(k, v)
		}
		resp, err := h.client.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return
		}
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64<<10), maxHTTPBody)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var m rpcMsg
			if json.Unmarshal([]byte(strings.TrimSpace(line[len("data:"):])), &m) != nil {
				continue
			}
			if m.Method == "" || isRequest(m) {
				continue // only interested in server-initiated notifications
			}
			select {
			case h.notifCh <- m.Method:
			default:
			}
		}
	}()
}

func (h *httpConn) notifications() <-chan string { return h.notifCh }

func (h *httpConn) close() error {
	if h.closed.Swap(true) {
		return nil
	}
	if h.streamCancel != nil {
		h.streamCancel()
		<-h.streamDone // guarantees the stream goroutine has stopped sending
	}
	close(h.notifCh)
	return nil
}
