package mcpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

// fakeHTTPServer is a Streamable HTTP fake, minimal enough to drive both eras and an
// SSE notification stream, mirroring what mcpreg/probe.go's probeHTTP already expects
// from a real one.
type fakeHTTPServer struct {
	mode string // "modern" | "legacy"

	mu      sync.Mutex
	version int
	sseCh   chan string // method names to push down the GET stream
}

func newFakeHTTPServer(t *testing.T, mode string) *httptest.Server {
	t.Helper()
	f := &fakeHTTPServer{mode: mode, version: 1, sseCh: make(chan string, 4)}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeHTTPServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		f.serveStream(w, r)
		return
	}
	var req map[string]any
	_ = json.NewDecoder(r.Body).Decode(&req)
	method, _ := req["method"].(string)
	id := req["id"]

	switch method {
	case "server/discover":
		if f.mode == "legacy" {
			f.writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "Method not found"}})
			return
		}
		f.writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
			"supportedVersions": []string{ProtocolVersion},
			"capabilities":      map[string]any{"tools": map[string]any{"listChanged": true}},
			"serverInfo":        map[string]any{"name": "fake-http-" + f.mode, "version": "1"},
		}})
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "sess-1")
		f.writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
			"protocolVersion": ProtocolVersionLegacy,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
			"serverInfo":      map[string]any{"name": "fake-http-" + f.mode, "version": "1"},
		}})
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		f.mu.Lock()
		v := f.version
		f.mu.Unlock()
		f.writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"tools": fakeTools(v)}})
	case "tools/call":
		f.handleCall(w, id, req)
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

func (f *fakeHTTPServer) handleCall(w http.ResponseWriter, id any, req map[string]any) {
	params, _ := req["params"].(map[string]any)
	name, _ := params["name"].(string)
	args, _ := params["arguments"].(map[string]any)
	switch name {
	case "echo":
		f.writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": fmt.Sprint(args["msg"])}}, "isError": false,
		}})
	case "boom":
		f.writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": "boom happened"}}, "isError": true,
		}})
	case "bump":
		f.mu.Lock()
		f.version = 2
		f.mu.Unlock()
		f.writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": "bumped"}}, "isError": false,
		}})
		select {
		case f.sseCh <- "notifications/tools/list_changed":
		default:
		}
	default:
		f.writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32602, "message": "unknown tool"}})
	}
}

func (f *fakeHTTPServer) serveStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		w.WriteHeader(http.StatusNotImplemented)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	for {
		select {
		case method := <-f.sseCh:
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (f *fakeHTTPServer) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func connectFakeHTTP(t *testing.T, mode string) *Server {
	t.Helper()
	srv := newFakeHTTPServer(t, mode)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Connect(ctx, mcpreg.ServerDef{Name: "fakehttp", Transport: mcpreg.TransportHTTP, URL: srv.URL})
	if err != nil {
		t.Fatalf("Connect(%s): %v", mode, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestHTTPStatelessEra_ListAndCall(t *testing.T) {
	s := connectFakeHTTP(t, "modern")
	if len(s.ToolDefs()) != 3 {
		t.Fatalf("ToolDefs = %+v, want 3 entries", s.ToolDefs())
	}
	args, _ := json.Marshal(map[string]any{"msg": "http-hi"})
	text, isErr, err := s.CallTool(context.Background(), "echo", args)
	if err != nil || isErr || text != "http-hi" {
		t.Fatalf("CallTool(echo) = (%q, %v, %v)", text, isErr, err)
	}
}

func TestHTTPLegacyEra_ListAndCall(t *testing.T) {
	s := connectFakeHTTP(t, "legacy")
	if len(s.ToolDefs()) != 3 {
		t.Fatalf("ToolDefs = %+v, want 3 entries", s.ToolDefs())
	}
	text, isErr, err := s.CallTool(context.Background(), "boom", nil)
	if err != nil {
		t.Fatalf("CallTool(boom): %v", err)
	}
	if !isErr || text != "boom happened" {
		t.Fatalf("CallTool(boom) = (%q, %v)", text, isErr)
	}
}

func TestHTTPListChanged_ViaSSEStream_RefreshesToolCache(t *testing.T) {
	s := connectFakeHTTP(t, "modern")
	if _, _, err := s.CallTool(context.Background(), "bump", nil); err != nil {
		t.Fatalf("CallTool(bump): %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, d := range s.ToolDefs() {
			if d.Name == PrefixToolName("fakehttp", "extra") {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("SSE notifications/tools/list_changed never refreshed the cache")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHTTPConnect_NoURL(t *testing.T) {
	_, err := Connect(context.Background(), mcpreg.ServerDef{Name: "x", Transport: mcpreg.TransportHTTP})
	if err == nil {
		t.Fatal("Connect with an empty URL should fail")
	}
}
