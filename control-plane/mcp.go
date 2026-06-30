package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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
		return rpcOK(req.ID, map[string]any{"tools": c.mcpToolList(ctx, prin)})
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
	admin    bool   // admin tool: requires super_admin / tenant_admin in the PAT's tenant
	schema   map[string]any
	// run executes a member/drive tool against the caller's own Workspace.
	run func(ctx context.Context, c config, res *resolved, args map[string]any) (string, error)
	// runAdmin executes an admin tool within the PAT's tenant (admin == true).
	runAdmin func(ctx context.Context, c config, ac *adminCtx, args map[string]any) (string, error)
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

// toolsFor returns the tools visible to a principal, filtered by scope and (for
// admin tools) by the live deployment/tenant role behind the PAT. role is the
// capability ceiling: a non-admin PAT never sees admin tools however high its
// scope, and a demotion takes effect immediately (role is resolved live, not
// frozen in the token). The capability filter is UX — authz is re-checked in
// mcpToolCall before any admin tool runs.
func (c config) toolsFor(ctx context.Context, prin *mcpPrincipal) []mcpTool {
	var out []mcpTool
	for _, t := range memberTools() {
		if scopeRank(prin.scope) >= scopeRank(t.minScope) {
			out = append(out, t)
		}
	}
	if ac, _ := c.adminPrincipal(ctx, prin); ac != nil && ac.isAdmin {
		for _, t := range adminTools() {
			if scopeRank(prin.scope) >= scopeRank(t.minScope) {
				out = append(out, t)
			}
		}
	}
	return out
}

func (c config) mcpToolList(ctx context.Context, prin *mcpPrincipal) []map[string]any {
	tools := c.toolsFor(ctx, prin)
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
	for _, t := range c.toolsFor(ctx, prin) {
		if t.name == p.Name {
			tt := t
			tool = &tt
			break
		}
	}
	if tool == nil {
		return rpcErr(req.ID, -32602, "unknown or unauthorized tool: "+p.Name)
	}

	var (
		text string
		err  error
	)
	if tool.admin {
		// Re-check authz in the service layer (the capability filter is UX, not
		// the authority): resolve the live role behind the PAT and require admin
		// of the token's tenant. The tenant is fixed by the token, never supplied.
		ac, aerr := c.adminPrincipal(ctx, prin)
		if aerr != nil {
			return rpcOK(req.ID, toolError(aerr.message))
		}
		if ac == nil || !ac.isAdmin {
			return rpcOK(req.ID, toolError("admin role required"))
		}
		text, err = tool.runAdmin(ctx, c, ac, p.Args)
	} else {
		// Resolve the caller's own Workspace from the PAT's membership (live role/
		// membership check; tenant fixed by the token, never client-supplied).
		res, aerr := c.mgr.resolveByMembership(ctx, prin.identityID, prin.membershipID)
		if aerr != nil {
			return rpcOK(req.ID, toolError(aerr.message))
		}
		text, err = tool.run(ctx, c, res, p.Args)
	}
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

func argInt(a map[string]any, k string) int {
	if a == nil {
		return 0
	}
	switch v := a[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

func jsonText(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// --- admin tools (P3-6) ---
//
// Admin tools wrap the CP admin service layer (no new logic). They are scoped to
// the tenant the PAT is bound to (never a client-supplied tenant) and gated on
// the live role behind the PAT: a deployment super_admin or a tenant_admin of
// that tenant. read tools observe; write tools mutate and append an AuditLog
// (actor_kind=mcp). The dangerous tier (rotate_key/stop_all_idle/...) is deferred
// until its substrate (key rotation, idle detection) exists.

// adminCtx is the resolved admin principal for one admin tool call: the live
// identity, the tenant fixed by the token, and whether the caller may administer
// it.
type adminCtx struct {
	prin    *mcpPrincipal
	ident   Identity
	tenant  Tenant
	isSuper bool
	isAdmin bool
}

// adminPrincipal resolves the live role behind a PAT and the tenant it is bound
// to. role is read fresh every call (never frozen in the token), so a demotion or
// membership removal takes effect immediately. Returns an apiError only on a real
// failure (DB error / unknown identity / inactive membership); a valid non-admin
// principal returns ac with isAdmin=false.
func (c config) adminPrincipal(ctx context.Context, prin *mcpPrincipal) (*adminCtx, *apiError) {
	ident, ok, err := c.mgr.store.GetIdentityByID(ctx, prin.identityID)
	if err != nil {
		return nil, internalErr(err)
	}
	if !ok {
		return nil, &apiError{http.StatusUnauthorized, "unauthenticated", "identity not found"}
	}
	ms, err := c.mgr.store.ListMemberships(ctx, prin.identityID)
	if err != nil {
		return nil, internalErr(err)
	}
	var mv MembershipView
	found := false
	for _, x := range ms {
		if x.MembershipID == prin.membershipID {
			mv, found = x, true
			break
		}
	}
	if !found {
		return nil, &apiError{http.StatusForbidden, "forbidden_tenant", "membership not active"}
	}
	t, err := c.mgr.store.GetTenant(ctx, mv.TenantID)
	if err != nil {
		return nil, internalErr(err)
	}
	isSuper := ident.Role == "super_admin"
	return &adminCtx{
		prin: prin, ident: ident, tenant: t,
		isSuper: isSuper,
		isAdmin: isSuper || mv.Role == "tenant_admin",
	}, nil
}

func adminTools() []mcpTool {
	userKeyArg := func(extra map[string]any, required ...string) map[string]any {
		props := map[string]any{
			"user_key": map[string]any{"type": "string", "description": "the member's user key (sanitized email)"},
		}
		for k, v := range extra {
			props[k] = v
		}
		return map[string]any{"type": "object", "properties": props, "required": required}
	}
	empty := map[string]any{"type": "object", "properties": map[string]any{}}
	return []mcpTool{
		{
			name: "list_workspaces", minScope: scopeRead, admin: true,
			desc:   "List your tenant's members with their Workspace container state (admin).",
			schema: empty,
			runAdmin: func(ctx context.Context, c config, ac *adminCtx, _ map[string]any) (string, error) {
				return c.mcpListWorkspaces(ctx, ac)
			},
		},
		{
			name: "get_usage", minScope: scopeRead, admin: true,
			desc:   "Tenant resource usage: member count, running Workspaces, quota limits (plus host load/memory for super_admin).",
			schema: empty,
			runAdmin: func(ctx context.Context, c config, ac *adminCtx, _ map[string]any) (string, error) {
				return c.mcpGetUsage(ctx, ac)
			},
		},
		{
			name: "list_sessions", minScope: scopeRead, admin: true,
			desc:   "List sessions across the tenant, or for one member when user_key is given (admin).",
			schema: userKeyArg(nil),
			runAdmin: func(ctx context.Context, c config, ac *adminCtx, a map[string]any) (string, error) {
				return c.mcpListSessions(ctx, ac, argStr(a, "user_key"))
			},
		},
		{
			name: "stop_workspace", minScope: scopeWrite, admin: true,
			desc:   "Force-stop a member's Workspace container (admin). The home persists; it restarts on next use.",
			schema: userKeyArg(nil, "user_key"),
			runAdmin: func(ctx context.Context, c config, ac *adminCtx, a map[string]any) (string, error) {
				return c.mcpStopWorkspace(ctx, ac, argStr(a, "user_key"))
			},
		},
		{
			name: "stop_session", minScope: scopeWrite, admin: true,
			desc: "Stop a running session in a member's Workspace (admin). The session stays resumable.",
			schema: userKeyArg(map[string]any{
				"name": map[string]any{"type": "string", "description": "session name"},
			}, "user_key", "name"),
			runAdmin: func(ctx context.Context, c config, ac *adminCtx, a map[string]any) (string, error) {
				return c.mcpStopSession(ctx, ac, argStr(a, "user_key"), argStr(a, "name"))
			},
		},
		{
			name: "set_user_quota", minScope: scopeWrite, admin: true,
			desc: "Set a member's per-user quota within the tenant (admin): max_sessions and disk_gb (0 = unset).",
			schema: userKeyArg(map[string]any{
				"max_sessions": map[string]any{"type": "integer", "description": "max concurrent sessions (0 = unset)"},
				"disk_gb":      map[string]any{"type": "integer", "description": "disk quota in GiB (0 = unset)"},
			}, "user_key"),
			runAdmin: func(ctx context.Context, c config, ac *adminCtx, a map[string]any) (string, error) {
				return c.mcpSetUserQuota(ctx, ac, argStr(a, "user_key"), argInt(a, "max_sessions"), argInt(a, "disk_gb"))
			},
		},
	}
}

// mcpResolveMember maps a user_key to its membership + workspace within the admin
// principal's tenant (the target of an admin tool). hasWS is false when the member
// has never started a workspace.
func (c config) mcpResolveMember(ctx context.Context, tenantID, key string) (Membership, Workspace, bool, error) {
	if strings.TrimSpace(key) == "" {
		return Membership{}, Workspace{}, false, fmt.Errorf("user_key required")
	}
	ident, err := c.mgr.store.UpsertIdentity(ctx, "", key, "")
	if err != nil {
		return Membership{}, Workspace{}, false, err
	}
	mem, ok, err := c.mgr.store.GetMembership(ctx, ident.ID, tenantID)
	if err != nil {
		return Membership{}, Workspace{}, false, err
	}
	if !ok {
		return Membership{}, Workspace{}, false, fmt.Errorf("%q is not a member of this tenant", key)
	}
	ws, hasWS, err := c.mgr.store.GetWorkspaceByMembership(ctx, mem.ID)
	if err != nil {
		return Membership{}, Workspace{}, false, err
	}
	return mem, ws, hasWS, nil
}

// mcpAudit records an admin write action (best-effort: a logging failure must not
// fail an action that already happened).
func (c config) mcpAudit(ctx context.Context, ac *adminCtx, action, target, detail string) {
	_ = c.mgr.store.InsertAudit(ctx, AuditLog{
		ID: newID(), TenantID: ac.tenant.ID, ActorKind: "mcp", ActorID: ac.prin.patID,
		Action: action, Target: target, Detail: detail, At: nowTS(),
	})
}

// mcpMemberSessions summarizes a member's sessions (Agent-authoritative while the
// container runs, DB mirror otherwise) — same precedence as the admin REST view.
func (c config) mcpMemberSessions(ctx context.Context, ws Workspace) []map[string]any {
	rt := c.mgr.runtimeFor(ws, "")
	if rt.state(ctx) == "running" {
		if list, err := c.mgr.agentSessions(ctx, rt); err == nil {
			out := make([]map[string]any, 0, len(list))
			for _, s := range list {
				out = append(out, map[string]any{"name": s.Name, "kind": s.Kind, "label": s.Label, "alive": s.Alive})
			}
			return out
		}
	}
	rows, err := c.mgr.store.ListSessions(ctx, ws.ID)
	if err != nil {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{"name": r.Name, "kind": r.Kind, "label": r.Label, "alive": false})
	}
	return out
}

func (c config) mcpListWorkspaces(ctx context.Context, ac *adminCtx) (string, error) {
	members, err := c.mgr.store.ListMembersByTenant(ctx, ac.tenant.ID)
	if err != nil {
		return "", err
	}
	rows := make([]map[string]any, 0, len(members))
	for _, m := range members {
		container, state := c.mgr.workspaceStateByMembership(ctx, m.MembershipID)
		rows = append(rows, map[string]any{
			"user_key": m.UserKey, "email": m.Email, "role": m.MemberRole,
			"container": container, "state": state,
		})
	}
	return jsonText(map[string]any{"tenant": ac.tenant.Slug, "workspaces": rows})
}

func (c config) mcpGetUsage(ctx context.Context, ac *adminCtx) (string, error) {
	members, err := c.mgr.store.ListMembersByTenant(ctx, ac.tenant.ID)
	if err != nil {
		return "", err
	}
	running, err := c.mgr.countRunningInTenant(ctx, ac.tenant.ID)
	if err != nil {
		return "", err
	}
	lim := parseLimits(ac.tenant.Limits)
	out := map[string]any{
		"tenant": ac.tenant.Slug, "users": len(members), "running_workspaces": running,
		"max_workspaces": lim.MaxWorkspaces, "max_sessions": lim.MaxSessions,
	}
	if ac.isSuper {
		load1, ncpu, memUsed, memTotal := readHostStats()
		out["host"] = map[string]any{"load1": load1, "ncpu": ncpu, "mem_used": memUsed, "mem_total": memTotal}
	}
	return jsonText(out)
}

func (c config) mcpListSessions(ctx context.Context, ac *adminCtx, userKey string) (string, error) {
	if strings.TrimSpace(userKey) != "" {
		_, ws, hasWS, err := c.mcpResolveMember(ctx, ac.tenant.ID, userKey)
		if err != nil {
			return "", err
		}
		if !hasWS {
			return jsonText(map[string]any{"user_key": userKey, "sessions": []any{}})
		}
		return jsonText(map[string]any{"user_key": userKey, "sessions": c.mcpMemberSessions(ctx, ws)})
	}
	members, err := c.mgr.store.ListMembersByTenant(ctx, ac.tenant.ID)
	if err != nil {
		return "", err
	}
	out := make([]map[string]any, 0, len(members))
	for _, m := range members {
		ws, hasWS, err := c.mgr.store.GetWorkspaceByMembership(ctx, m.MembershipID)
		if err != nil || !hasWS {
			continue
		}
		out = append(out, map[string]any{"user_key": m.UserKey, "sessions": c.mcpMemberSessions(ctx, ws)})
	}
	return jsonText(map[string]any{"tenant": ac.tenant.Slug, "members": out})
}

func (c config) mcpStopWorkspace(ctx context.Context, ac *adminCtx, userKey string) (string, error) {
	mem, _, hasWS, err := c.mcpResolveMember(ctx, ac.tenant.ID, userKey)
	if err != nil {
		return "", err
	}
	if !hasWS {
		return "", fmt.Errorf("%q has no workspace", userKey)
	}
	if err := c.mgr.stopWorkspaceByMembership(ctx, mem.ID); err != nil {
		return "", err
	}
	c.mcpAudit(ctx, ac, "stop_workspace", userKey, "tenant="+ac.tenant.Slug)
	return jsonText(map[string]any{"stopped": userKey, "tenant": ac.tenant.Slug})
}

func (c config) mcpStopSession(ctx context.Context, ac *adminCtx, userKey, name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("name required")
	}
	_, ws, hasWS, err := c.mcpResolveMember(ctx, ac.tenant.ID, userKey)
	if err != nil {
		return "", err
	}
	if !hasWS {
		return "", fmt.Errorf("%q has no workspace", userKey)
	}
	rt := c.mgr.runtimeFor(ws, "")
	if rt.state(ctx) != "running" {
		return "", fmt.Errorf("%q workspace is not running", userKey)
	}
	text, err := agentText(ctx, rt, "POST", "/sessions/"+url.PathEscape(name)+"/halt", nil)
	if err != nil {
		return "", err
	}
	c.mcpAudit(ctx, ac, "stop_session", userKey+"/"+name, "tenant="+ac.tenant.Slug)
	return text, nil
}

func (c config) mcpSetUserQuota(ctx context.Context, ac *adminCtx, userKey string, maxSessions, diskGB int) (string, error) {
	mem, _, _, err := c.mcpResolveMember(ctx, ac.tenant.ID, userKey)
	if err != nil {
		return "", err
	}
	if err := c.mgr.store.PutUserLimit(ctx, mem.ID, maxSessions, diskGB); err != nil {
		return "", err
	}
	c.mcpAudit(ctx, ac, "set_user_quota", userKey, fmt.Sprintf("max_sessions=%d disk_gb=%d", maxSessions, diskGB))
	return jsonText(map[string]any{"user_key": userKey, "tenant": ac.tenant.Slug, "max_sessions": maxSessions, "disk_gb": diskGB})
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
