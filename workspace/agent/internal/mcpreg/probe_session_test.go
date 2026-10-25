package mcpreg

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// legacyFake is a legacy-era (initialize + Mcp-Session-Id) server. reject decides, per
// session-bound request (1-based count, method), whether it answers as an instance that
// does not know the session id.
type legacyFake struct {
	issueID bool
	status  int    // status for a rejected request
	body    string // body for a rejected request
	reject  func(n int, method string) bool
	delay   time.Duration // per-request latency

	mu sync.Mutex
	n  int
}

func (f *legacyFake) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&m)
		time.Sleep(f.delay)
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		switch m.Method {
		case "server/discover":
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID,
				"error": map[string]any{"code": -32601, "message": "method not found"}})
			return
		case "initialize":
			if f.issueID {
				w.Header().Set("Mcp-Session-Id", "sess-1")
			}
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID,
				"result": map[string]any{"serverInfo": map[string]any{"name": "old", "version": "0.1"}}})
			return
		}
		f.mu.Lock()
		f.n++
		n := f.n
		f.mu.Unlock()
		if f.reject != nil && f.reject(n, m.Method) {
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(f.body))
			return
		}
		if m.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID,
			"result": map[string]any{"tools": []map[string]any{{"name": "t"}}}})
	})
}

func probeLegacy(t *testing.T, f *legacyFake) ProbeResult {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	return Probe(context.Background(), httpDef("remote", srv.URL))
}

func TestProbeHTTPSessionRejected(t *testing.T) {
	const jsonErr = `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"Session not found"}}`
	cases := []struct {
		name   string
		status int
		body   string
		reject func(n int, method string) bool
	}{
		{"404 on tools/list", 404, "Session not found", func(n int, _ string) bool { return n >= 2 }},
		{"400 on tools/list", 400, jsonErr, func(n int, _ string) bool { return n >= 2 }},
		{"404 on initialized", 404, "Session not found", func(n int, _ string) bool { return n == 1 }},
		{"400 on initialized", 400, jsonErr, func(n int, _ string) bool { return n == 1 }},
		// Round-robin over two instances: every 2nd request lands on the one that
		// does not know the id. One tools/list alone would pass by luck.
		{"every 2nd request, 404", 404, "Session not found", func(n int, _ string) bool { return n%2 == 0 }},
		{"every 2nd request, 400", 400, jsonErr, func(n int, _ string) bool { return n%2 == 0 }},
		{"every 2nd request, tools/list only", 404, "Session not found", func(n int, m string) bool { return m == "tools/list" && n%2 == 1 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := probeLegacy(t, &legacyFake{issueID: true, status: c.status, body: c.body, reject: c.reject})
			if res.OK {
				t.Fatalf("a rejected session id was treated as success: %+v", res)
			}
			if res.Code != codeSessionRejected || res.Error != errSessionRejected {
				t.Fatalf("error = %q, want the dedicated session message", res.Error)
			}
			if !strings.Contains(res.Detail, "HTTP "+strconv.Itoa(c.status)) || !strings.Contains(res.Detail, "Session") {
				t.Fatalf("detail lacks status/body: %q", res.Detail)
			}
		})
	}
}

// A 400 that is not about the session is an ordinary HTTP failure, not the affinity verdict.
func TestProbeHTTPUnrelated400IsNotSessionVerdict(t *testing.T) {
	res := probeLegacy(t, &legacyFake{issueID: true, status: 400, body: "bad params",
		reject: func(n int, _ string) bool { return n >= 2 }})
	if res.OK || res.Error != "HTTP 400" {
		t.Fatalf("got %+v", res)
	}
}

// A 404 on the notification of a server that never issued an id is a plain HTTP failure.
func TestProbeHTTPInitializedNon2xxFailsWithoutSession(t *testing.T) {
	res := probeLegacy(t, &legacyFake{status: 404, body: "gone",
		reject: func(n int, m string) bool { return m == "notifications/initialized" }})
	if res.OK || res.Error != "HTTP 404" {
		t.Fatalf("got %+v", res)
	}
}

// A stateful server that keeps its sessions still passes, with the 202 ack accepted.
func TestProbeHTTPStatefulHealthyPasses(t *testing.T) {
	f := &legacyFake{issueID: true}
	res := probeLegacy(t, f)
	if !res.OK || res.ToolCount != 1 {
		t.Fatalf("healthy stateful server failed: %+v", res)
	}
	if f.n < 4 {
		t.Fatalf("only %d session-bound requests were sent", f.n)
	}
}

// A legacy server that issues no id needs no extra calls: notification + one tools/list.
func TestProbeHTTPLegacyWithoutSessionIDSendsNoExtraCalls(t *testing.T) {
	f := &legacyFake{}
	res := probeLegacy(t, f)
	if !res.OK {
		t.Fatalf("sessionless legacy server failed: %+v", res)
	}
	if f.n != 2 {
		t.Fatalf("session-bound request count = %d, want 2", f.n)
	}
}

// Java-style exception JSON: valid JSON, but not a JSON-RPC message.
const javaSessionErr = `{"cause":null,"jsonRpcError":null,"localizedMessage":"Session not found: sess-1","message":"Session not found: sess-1"}`

func TestProbeHTTPSessionRejectedNonRPCBody(t *testing.T) {
	res := probeLegacy(t, &legacyFake{issueID: true, status: 404, body: javaSessionErr,
		reject: func(n int, _ string) bool { return n >= 2 }})
	if res.OK || res.Code != codeSessionRejected || res.Error != errSessionRejected || !strings.Contains(res.Detail, "HTTP 404") {
		t.Fatalf("got %+v", res)
	}
}

// A balancer that pins each TCP connection to one of two instances with in-memory
// sessions, assigning new connections round-robin. Reusing one kept-alive connection
// always reaches the issuing instance and passes; fresh connections do not.
func TestProbeHTTPSessionAffinityNeedsFreshConnections(t *testing.T) {
	var mu sync.Mutex
	backendOf := map[string]int{} // RemoteAddr -> backend
	sessions := [2]map[string]bool{{}, {}}
	next := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&m)
		mu.Lock()
		b, ok := backendOf[r.RemoteAddr]
		if !ok {
			b = next % 2
			next++
			backendOf[r.RemoteAddr] = b
		}
		known := sessions[b][r.Header.Get("Mcp-Session-Id")]
		if m.Method == "initialize" {
			sessions[b]["sess-1"] = true
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch m.Method {
		case "server/discover":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID,
				"error": map[string]any{"code": -32601, "message": "method not found"}})
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID,
				"result": map[string]any{"serverInfo": map[string]any{"name": "old"}}})
		default:
			if !known {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(javaSessionErr))
				return
			}
			if m.Method == "notifications/initialized" {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID,
				"result": map[string]any{"tools": []map[string]any{{"name": "t"}}}})
		}
	}))
	defer srv.Close()

	res := Probe(context.Background(), httpDef("remote", srv.URL))
	if res.OK || res.Code != codeSessionRejected || res.Error != errSessionRejected {
		t.Fatalf("the instance without the session went unnoticed: %+v", res)
	}
}

// A stateless-era server whose tools/list is a 503 with a plain-text body is a failure.
func TestProbeHTTPStatelessToolsListNon2xxFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&m)
		if m.Method == "server/discover" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID,
				"result": map[string]any{"supportedVersions": []string{"2026-07-28"}}})
			return
		}
		http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	res := Probe(context.Background(), httpDef("remote", srv.URL))
	if res.OK || res.Error != "HTTP 503" || !strings.Contains(res.Detail, "upstream unavailable") {
		t.Fatalf("got %+v", res)
	}
}

// A healthy but slow single-instance server must not fail because of the extra checks.
func TestProbeHTTPSlowStatefulServerStillPasses(t *testing.T) {
	f := &legacyFake{issueID: true, delay: 400 * time.Millisecond}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	d := httpDef("remote", srv.URL)
	d.TimeoutMS = 2500
	res := Probe(context.Background(), d)
	if !res.OK || res.ToolCount != 1 {
		t.Fatalf("slow healthy server failed: %+v", res)
	}
}

// Rate limiting that hits only the extra checks is inconclusive, not a failure.
func TestProbeHTTPExtraCheckRateLimitedStillPasses(t *testing.T) {
	res := probeLegacy(t, &legacyFake{issueID: true, status: 429, body: "slow down",
		reject: func(n int, _ string) bool { return n >= 3 }})
	if !res.OK {
		t.Fatalf("got %+v", res)
	}
}

// A 400 echoing the session id next to an unrelated complaint is not an unknown session.
func TestProbeHTTPSessionIDEchoIsNotSessionVerdict(t *testing.T) {
	res := probeLegacy(t, &legacyFake{issueID: true, status: 400,
		body:   `{"error":"invalid params","request":{"method":"tools/list"},"sessionId":"sess-1"}`,
		reject: func(n int, _ string) bool { return n >= 2 }})
	if res.OK || res.Error != "HTTP 400" {
		t.Fatalf("got %+v", res)
	}
}

// The status survives a long body: the Detail keeps the HTTP number.
func TestProbeHTTPSessionRejectedDetailKeepsStatusForLongBody(t *testing.T) {
	body := `{"message":"Session not found"}` + strings.Repeat(" ", 2000) + "END"
	res := probeLegacy(t, &legacyFake{issueID: true, status: 404, body: body,
		reject: func(n int, _ string) bool { return n >= 2 }})
	if res.Code != codeSessionRejected || res.Error != errSessionRejected || !strings.HasPrefix(res.Detail, "HTTP 404: ") {
		t.Fatalf("got %+v", res)
	}
}
