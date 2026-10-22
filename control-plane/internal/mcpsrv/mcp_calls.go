package mcpsrv

// Observability of /mcp (docs/decisions/0118-mcp-oauth.md, issue #2531). Every request leaves
// one structured log line and, when the caller authenticated, one entry in a bounded in-memory
// ring of that PAT / OAuth grant, which its owning member reads back through the member API.
//
// What is recorded is the SHAPE of a request: methods, protocol era and versions, which
// negotiation headers were present, the status and the JSON-RPC error. Never a token, an
// Authorization value, request arguments or tool results — a record is built only from the
// fields below, and nothing in this file accepts a body or a header map wholesale.

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// callRingSize is how many recent requests one PAT / grant keeps.
	callRingSize = 50
	// callMaxKeys bounds how many PATs / grants hold a ring at once; the one idle longest is
	// evicted first. Worst case: a record is at most ~5 KiB (callMaxItems items of ~0.55 KiB
	// plus ~0.7 KiB of request fields, every string clipped and cloned), so
	// 5 KiB × callRingSize × callMaxKeys ≈ 75 MiB.
	callMaxKeys = 300
	// callMaxItems caps the JSON-RPC items kept per request (a batch is otherwise unbounded).
	callMaxItems = 8
)

// CallItem is one JSON-RPC message of a request (a batch has several).
type CallItem struct {
	Method string `json:"method"`
	// Name is the tool / prompt name of a tools/call or prompts/get (the value Mcp-Name mirrors).
	// The arguments next to it are never read.
	Name string `json:"name,omitempty"`
	// Era is "stateless" (2026-07-28, `_meta` per request) or "legacy" (initialize handshake).
	Era         string `json:"era,omitempty"`
	MetaVersion string `json:"meta_version,omitempty"`
	// ClientName / ClientVersion are the client's own declaration: `_meta` clientInfo, or
	// params.clientInfo of initialize. Informational; nothing trusts them.
	ClientName    string `json:"client_name,omitempty"`
	ClientVersion string `json:"client_version,omitempty"`
	// Status is the HTTP status this item alone would have carried (a batch answers 200 as a whole).
	Status       int  `json:"status"`
	Notification bool `json:"notification,omitempty"`
	// ToolError marks a tools/call answered 200 with isError (the call ran and failed). Only the
	// flag is kept; the result text is never read.
	ToolError    bool   `json:"tool_error,omitempty"`
	ErrorCode    int    `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	// Tools is the number of tools a tools/list returned.
	Tools *int `json:"tools,omitempty"`
}

// CallRecord is one /mcp request as the member and the operator see it.
type CallRecord struct {
	At         string `json:"at"`
	HTTPMethod string `json:"http_method"`
	Status     int    `json:"status"`
	LatencyMs  int64  `json:"latency_ms"`
	// Principal is "pat:<id>", "grant:<id>" or "unauthenticated".
	Principal string `json:"principal"`
	ClientID  string `json:"client_id,omitempty"`
	// Scope is the PAT / grant scope: a short tools/list may just be a narrow scope.
	Scope string `json:"scope,omitempty"`
	// Reason says why a request was refused before it reached a tool.
	Reason string `json:"reason,omitempty"`
	// HeaderVersion is the MCP-Protocol-Version header; the `_meta` one is per item.
	HeaderVersion string `json:"header_version,omitempty"`
	// McpMethodHeader / McpNameHeader are presence only: the stateless era requires them to
	// mirror the body, and a client omitting one is rejected with -32020.
	McpMethodHeader bool       `json:"mcp_method_header"`
	McpNameHeader   bool       `json:"mcp_name_header"`
	Accept          string     `json:"accept,omitempty"`
	ContentType     string     `json:"content_type,omitempty"`
	UserAgent       string     `json:"user_agent,omitempty"`
	Items           []CallItem `json:"items"`
	// ItemsOmitted counts items beyond callMaxItems.
	ItemsOmitted int `json:"items_omitted,omitempty"`
}

// CallLog is the per-replica ring buffers; the zero value is ready to use.
// A nil *CallLog drops everything, so a caller without one (unit tests) needs no guard.
type CallLog struct {
	mu    sync.Mutex
	rings map[string]*callRing
}

type callRing struct {
	recs []CallRecord // oldest first, at most callRingSize
	last time.Time
}

// Key names the ring of a PAT or an OAuth grant. The two id spaces are separate, hence the prefix.
func CallKey(oauth bool, id string) string {
	if oauth {
		return "grant:" + id
	}
	return "pat:" + id
}

func (l *CallLog) add(key string, rec CallRecord) {
	if l == nil || key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rings == nil {
		l.rings = map[string]*callRing{}
	}
	rg := l.rings[key]
	if rg == nil {
		if len(l.rings) >= callMaxKeys {
			l.evictIdlest()
		}
		rg = &callRing{}
		l.rings[key] = rg
	}
	if len(rg.recs) >= callRingSize {
		copy(rg.recs, rg.recs[1:])
		rg.recs = rg.recs[:callRingSize-1]
	}
	rg.recs = append(rg.recs, rec)
	rg.last = time.Now()
}

func (l *CallLog) evictIdlest() {
	var oldest string
	var at time.Time
	for k, rg := range l.rings {
		if oldest == "" || rg.last.Before(at) {
			oldest, at = k, rg.last
		}
	}
	delete(l.rings, oldest)
}

// Forget drops the ring of key; a revoked PAT / grant has no reader and would only hold a slot.
func (l *CallLog) Forget(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	delete(l.rings, key)
	l.mu.Unlock()
}

// Recent returns the ring of key, newest first. The slice is a copy; each record's Items are shared but never mutated after add.
func (l *CallLog) Recent(key string) []CallRecord {
	out := []CallRecord{}
	if l == nil {
		return out
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	rg := l.rings[key]
	if rg == nil {
		return out
	}
	for i := len(rg.recs) - 1; i >= 0; i-- {
		out = append(out, rg.recs[i])
	}
	return out
}

// --- building a record ---

// callTrace collects what one request shows while it is handled.
type callTrace struct {
	start time.Time
	rec   CallRecord
	key   string // ring key; empty until the caller authenticates
}

func newCallTrace(r *http.Request) *callTrace {
	return &callTrace{start: time.Now(), rec: CallRecord{
		HTTPMethod:      clip(r.Method, 16),
		Principal:       "unauthenticated",
		HeaderVersion:   clip(r.Header.Get("MCP-Protocol-Version"), 40),
		McpMethodHeader: r.Header.Get("Mcp-Method") != "",
		McpNameHeader:   r.Header.Get("Mcp-Name") != "",
		Accept:          clip(r.Header.Get("Accept"), 120),
		ContentType:     clip(r.Header.Get("Content-Type"), 120),
		UserAgent:       clip(r.UserAgent(), 200),
		Items:           []CallItem{},
	}}
}

func (t *callTrace) setPrincipal(p *mcpPrincipal) {
	t.key = CallKey(p.oauth, p.patID)
	t.rec.Principal = t.key
	t.rec.ClientID = clip(p.clientID, 100)
	t.rec.Scope = clip(p.scope, 32)
}

// addParseError records a body that was not JSON-RPC at all (answered 200 with -32700).
func (t *callTrace) addParseError(msg string) {
	t.addItem(CallItem{Status: http.StatusOK, ErrorCode: -32700, ErrorMessage: msg})
}

func (t *callTrace) addItem(it CallItem) {
	if len(t.rec.Items) >= callMaxItems {
		t.rec.ItemsOmitted++
		return
	}
	t.rec.Items = append(t.rec.Items, it)
}

// itemFor describes one dispatched message. resp is nil for a notification.
func itemFor(req rpcRequest, p paramsMeta, era mcpEra, resp *rpcResponse, status int) CallItem {
	it := CallItem{Method: clip(req.Method, 64), Era: "legacy", Status: status, Notification: len(req.ID) == 0}
	if era.Stateless {
		it.Era, it.MetaVersion = "stateless", clip(era.Version, 32)
	}
	if req.Method == "tools/call" || req.Method == "prompts/get" {
		it.Name = clip(p.Name, 80)
	}
	it.ClientName, it.ClientVersion = clientDeclared(req, p)
	if resp != nil && resp.Error != nil {
		it.ErrorCode, it.ErrorMessage = resp.Error.Code, clip(resp.Error.Message, 120)
		// A Mcp-Name mismatch error quotes the body's resource URI, which is an argument.
		if req.Method == "resources/read" {
			it.ErrorMessage = ""
		}
	}
	if resp != nil && req.Method == "tools/call" {
		if m, ok := resp.Result.(map[string]any); ok {
			it.ToolError, _ = m["isError"].(bool)
		}
	}
	if resp != nil && req.Method == "tools/list" {
		if m, ok := resp.Result.(map[string]any); ok {
			if tl, ok := m["tools"].([]map[string]any); ok {
				n := len(tl)
				it.Tools = &n
			}
		}
	}
	return it
}

// clientDeclared reads the client's name and version from `_meta` or, for initialize, from its params.
func clientDeclared(req rpcRequest, p paramsMeta) (name, version string) {
	var ci struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if raw, ok := p.Meta[metaClientInfo]; ok && json.Unmarshal(raw, &ci) == nil && ci.Name != "" {
		return clip(ci.Name, 64), clip(ci.Version, 32)
	}
	if req.Method == "initialize" {
		var ip struct {
			ClientInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"clientInfo"`
		}
		if json.Unmarshal(req.Params, &ip) == nil {
			return clip(ip.ClientInfo.Name, 64), clip(ip.ClientInfo.Version, 32)
		}
	}
	return "", ""
}

// finish stamps status and latency, writes the log line and fills the ring.
func (a API) finish(t *callTrace, status int) {
	t.rec.At = t.start.UTC().Format(time.RFC3339Nano)
	t.rec.Status = status
	t.rec.LatencyMs = time.Since(t.start).Milliseconds()
	log.Print(callLogLine(t.rec))
	if a.cp != nil {
		a.cp.CallLog().add(t.key, t.rec)
	}
}

// callLogLine renders a record as one line: fixed key=value pairs for grepping, then the items as JSON.
func callLogLine(c CallRecord) string {
	var b strings.Builder
	b.WriteString("mcp-call")
	kv := func(k, v string) { b.WriteString(" " + k + "=" + strconv.Quote(v)) }
	kv("http_method", c.HTTPMethod)
	b.WriteString(" status=" + strconv.Itoa(c.Status))
	b.WriteString(" latency_ms=" + strconv.FormatInt(c.LatencyMs, 10))
	kv("principal", c.Principal)
	if c.ClientID != "" {
		kv("client_id", c.ClientID)
	}
	if c.Scope != "" {
		kv("scope", c.Scope)
	}
	if c.Reason != "" {
		kv("reason", c.Reason)
	}
	kv("header_version", c.HeaderVersion)
	b.WriteString(" mcp_method_header=" + strconv.FormatBool(c.McpMethodHeader))
	b.WriteString(" mcp_name_header=" + strconv.FormatBool(c.McpNameHeader))
	kv("accept", c.Accept)
	kv("content_type", c.ContentType)
	kv("user_agent", c.UserAgent)
	items, _ := json.Marshal(c.Items)
	b.WriteString(" items=" + string(items))
	return b.String()
}

// clip bounds a client-supplied string and returns a copy that owns its bytes: a substring would
// keep the whole request body or header value alive inside the ring (measured: 50 requests with a
// 1 MiB method pinned 49 MiB). It cuts on a rune boundary.
func clip(s string, n int) string {
	if len(s) > n {
		for n > 0 && (s[n]&0xC0) == 0x80 {
			n--
		}
		s = s[:n]
	}
	return strings.Clone(s)
}

// statusWriter remembers the status the handler answered with.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}
