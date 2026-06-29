package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MCP server (docs/decisions/0006, P3-6). A single Streamable-HTTP endpoint at
// /mcp exposes Fleet tools to a Claude client (Claude Code / Desktop). Phase 1
// ships the member/drive tools (E): a user's own Claude drives the claude
// sessions in their Workspace. Admin tools follow in later phases.
//
// Transport: minimal Streamable HTTP — the client POSTs JSON-RPC 2.0 and we
// answer with application/json (no SSE; streaming is a later optimization, plan
// §2.3). Auth: Bearer PAT on every request, resolved to identity+membership with
// role/scope; tenant is fixed by the token (no client-supplied tenant). Gated by
// AF_MCP_ENABLED so deployments opt in.

const mcpProtocolVersion = "2025-06-18"

// mcpPrincipal is the resolved caller behind a PAT for one MCP request.
type mcpPrincipal struct {
	patID, identityID, membershipID, scope string
}

// --- JSON-RPC 2.0 ---

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // absent => notification
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func rpcOK(id json.RawMessage, result any) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}
func rpcErr(id json.RawMessage, code int, msg string) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{code, msg}}
}

// handleMCP is the /mcp endpoint. Registered only when AF_MCP_ENABLED=true.
func (c config) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet || r.Method == http.MethodDelete {
		// No server-initiated stream / session teardown in the minimal server.
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	prin, aerr := c.authMCP(r)
	if aerr != nil {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeAPIErr(w, aerr)
		return
	}

	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		writeJSON(w, http.StatusOK, rpcErr(nil, -32700, "empty request"))
		return
	}

	// Batch (array) vs single object.
	if trimmed[0] == '[' {
		var reqs []rpcRequest
		if err := json.Unmarshal(trimmed, &reqs); err != nil {
			writeJSON(w, http.StatusOK, rpcErr(nil, -32700, "parse error"))
			return
		}
		var out []*rpcResponse
		for _, req := range reqs {
			if resp := c.dispatchMCP(r.Context(), prin, req); resp != nil {
				out = append(out, resp)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(trimmed, &req); err != nil {
		writeJSON(w, http.StatusOK, rpcErr(nil, -32700, "parse error"))
		return
	}
	resp := c.dispatchMCP(r.Context(), prin, req)
	if resp == nil { // notification
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// authMCP resolves the Bearer PAT to a principal, rejecting missing/unknown/
// revoked/expired tokens. role is resolved live downstream (per call).
func (c config) authMCP(r *http.Request) (*mcpPrincipal, *apiError) {
	auth := r.Header.Get("Authorization")
	tok := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	if tok == "" || tok == auth { // no/!Bearer prefix
		return nil, &apiError{http.StatusUnauthorized, "unauthenticated", "missing bearer token"}
	}
	p, ok, err := c.mgr.store.GetPATByHash(r.Context(), hashPAT(tok))
	if err != nil {
		return nil, internalErr(err)
	}
	if !ok || p.RevokedAt != "" {
		return nil, &apiError{http.StatusUnauthorized, "unauthenticated", "invalid token"}
	}
	if p.ExpiresAt != "" {
		if exp, err := time.Parse(time.RFC3339, p.ExpiresAt); err == nil && time.Now().After(exp) {
			return nil, &apiError{http.StatusUnauthorized, "unauthenticated", "token expired"}
		}
	}
	_ = c.mgr.store.TouchPAT(r.Context(), p.ID) // best-effort last-used
	return &mcpPrincipal{patID: p.ID, identityID: p.IdentityID, membershipID: p.MembershipID, scope: p.Scope}, nil
}

func (c config) dispatchMCP(ctx context.Context, prin *mcpPrincipal, req rpcRequest) *rpcResponse {
	isNotification := len(req.ID) == 0
	switch req.Method {
	case "initialize":
		// Echo a compatible protocol version; advertise tools capability.
		ver := mcpProtocolVersion
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion != "" {
			ver = p.ProtocolVersion
		}
		return rpcOK(req.ID, map[string]any{
			"protocolVersion": ver,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "agent-fleet", "version": "p3-6"},
		})
	case "notifications/initialized", "notifications/cancelled":
		return nil // notifications get no response
	case "ping":
		return rpcOK(req.ID, map[string]any{})
	case "tools/list":
		return rpcOK(req.ID, map[string]any{"tools": c.mcpToolList(prin)})
	case "tools/call":
		return c.mcpToolCall(ctx, prin, req)
	default:
		if isNotification {
			return nil
		}
		return rpcErr(req.ID, -32601, "method not found: "+req.Method)
	}
}

// --- tool registry ---

type mcpTool struct {
	name     string
	desc     string
	minScope string // read | write | admin:dangerous
	schema   map[string]any
	run      func(ctx context.Context, c config, res *resolved, args map[string]any) (string, error)
}

// memberTools are the Phase-1 member/drive tools (E). Each runs against the
// caller's own Workspace, resolved from the PAT's membership.
func memberTools() []mcpTool {
	nameArg := map[string]any{
		"type":       "object",
		"properties": map[string]any{"name": map[string]any{"type": "string", "description": "session name"}},
		"required":   []string{"name"},
	}
	return []mcpTool{
		{
			name: "list_my_sessions", minScope: scopeRead,
			desc:   "List the Claude/opencode/codex sessions in your Workspace (name, kind, alive, status).",
			schema: map[string]any{"type": "object", "properties": map[string]any{}},
			run: func(ctx context.Context, c config, res *resolved, _ map[string]any) (string, error) {
				return agentText(ctx, res.rt, "GET", "/sessions", nil)
			},
		},
		{
			name: "get_session_status", minScope: scopeRead,
			desc:   "Get a session's live state: working | idle (awaiting input) | question (AskUserQuestion).",
			schema: nameArg,
			run: func(ctx context.Context, c config, res *resolved, a map[string]any) (string, error) {
				return agentText(ctx, res.rt, "GET", "/sessions/"+url.PathEscape(argStr(a, "name"))+"/status", nil)
			},
		},
		{
			name: "get_session_output", minScope: scopeRead,
			desc: "Read the session's assistant output since an optional cursor. Returns {output, cursor, status}; poll until status is idle or question.",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":  map[string]any{"type": "string", "description": "session name"},
					"since": map[string]any{"type": "string", "description": "cursor from a previous call (optional)"},
				},
				"required": []string{"name"},
			},
			run: func(ctx context.Context, c config, res *resolved, a map[string]any) (string, error) {
				q := ""
				if s := argStr(a, "since"); s != "" {
					q = "?since=" + url.QueryEscape(s)
				}
				return agentText(ctx, res.rt, "GET", "/sessions/"+url.PathEscape(argStr(a, "name"))+"/output"+q, nil)
			},
		},
		{
			name: "send_to_session", minScope: scopeWrite,
			desc: "Send a prompt to a session (drives the remote Claude). Returns immediately; poll get_session_status then get_session_output for the reply.",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":   map[string]any{"type": "string", "description": "session name"},
					"prompt": map[string]any{"type": "string", "description": "the prompt text to send"},
				},
				"required": []string{"name", "prompt"},
			},
			run: func(ctx context.Context, c config, res *resolved, a map[string]any) (string, error) {
				body, _ := json.Marshal(map[string]string{"prompt": argStr(a, "prompt")})
				return agentText(ctx, res.rt, "POST", "/sessions/"+url.PathEscape(argStr(a, "name"))+"/input", body)
			},
		},
	}
}

// toolsFor returns the tools visible to a principal (scope-filtered). Admin tools
// (later phases) will additionally gate on identity.role here.
func (c config) toolsFor(prin *mcpPrincipal) []mcpTool {
	var out []mcpTool
	for _, t := range memberTools() {
		if scopeRank(prin.scope) >= scopeRank(t.minScope) {
			out = append(out, t)
		}
	}
	return out
}

func (c config) mcpToolList(prin *mcpPrincipal) []map[string]any {
	tools := c.toolsFor(prin)
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{"name": t.name, "description": t.desc, "inputSchema": t.schema})
	}
	return out
}

func (c config) mcpToolCall(ctx context.Context, prin *mcpPrincipal, req rpcRequest) *rpcResponse {
	var p struct {
		Name string         `json:"name"`
		Args map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return rpcErr(req.ID, -32602, "invalid params")
	}
	var tool *mcpTool
	for _, t := range c.toolsFor(prin) {
		if t.name == p.Name {
			tt := t
			tool = &tt
			break
		}
	}
	if tool == nil {
		return rpcErr(req.ID, -32602, "unknown or unauthorized tool: "+p.Name)
	}
	// Resolve the caller's own Workspace from the PAT's membership (live role/
	// membership check; tenant fixed by the token, never client-supplied).
	res, aerr := c.mgr.resolveByMembership(ctx, prin.identityID, prin.membershipID)
	if aerr != nil {
		return rpcOK(req.ID, toolError(aerr.message))
	}
	text, err := tool.run(ctx, c, res, p.Args)
	if err != nil {
		return rpcOK(req.ID, toolError(err.Error()))
	}
	return rpcOK(req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	})
}

// toolError is a tools/call result flagged isError (MCP convention: execution
// failures are results, not protocol errors).
func toolError(msg string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": msg}},
		"isError": true,
	}
}

func argStr(a map[string]any, k string) string {
	if a == nil {
		return ""
	}
	if v, ok := a[k].(string); ok {
		return v
	}
	return ""
}

// agentText performs an authenticated CP→Agent request and returns the body as
// text (the Agent already returns JSON; we pass it through to the model).
func agentText(ctx context.Context, rt *dockerRuntime, method, path string, body []byte) (string, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rt.agentBase()+path, r)
	if err != nil {
		return "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if rt.token != "" {
		req.Header.Set("Authorization", "Bearer "+rt.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("workspace agent unreachable (is the workspace running?)")
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("agent %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return string(b), nil
}
