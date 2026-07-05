package main

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"time"
)

// Claude self-operation audit (docs/20 M5, A-第2段). claude runs untrusted inside the
// container and edits files / runs Bash directly; those don't flow through the CP proxy
// (M1), so they'd be invisible. The isolation model blocks Agent→CP, so instead of a
// push from claude's PreToolUse hook the CP PULLS: a background sweeper reads each
// running claude session's transcript (CP→Agent, the normal direction) and records the
// Write/Edit/Bash tool_use into the audit ledger with actor_kind=claude, attributed to
// the workspace's tenant. A per-session cursor (deployment_setting kv) makes it
// incremental; the first sighting only baselines the cursor so pre-existing history
// isn't audited retroactively. Opt-in: AF_CLAUDE_AUDIT_INTERVAL=0 (default) disables it.

// claudeAuditTools maps a claude tool name to the audit action it records. Read/Grep/
// etc. are intentionally absent — M5 records change/exec operations only (docs/20 §E.1).
var claudeAuditTools = map[string]string{
	"Write":        "claude.write",
	"Edit":         "claude.edit",
	"MultiEdit":    "claude.edit",
	"NotebookEdit": "claude.edit",
	"Bash":         "claude.bash",
}

// ctPart / ctTurn / ctResp are the subset of the Agent's GET /sessions/{name}/messages
// response (session_transcript.go chatPart/chatTurn) the auditor needs.
type ctPart struct {
	Kind string `json:"kind"`
	Tool string `json:"tool"`
	Info string `json:"info"`
	File string `json:"file"`
}
type ctTurn struct {
	Role  string   `json:"role"`
	Parts []ctPart `json:"parts"`
	TS    string   `json:"ts"`
}
type ctResp struct {
	Messages []ctTurn `json:"messages"`
	Cursor   int      `json:"cursor"`
	Reset    bool     `json:"reset"`
}

// extractClaudeAudits turns transcript turns into audit rows (pure, for testing). Only
// assistant turns' change/exec tool_use parts count; ID/At are filled by the caller.
func extractClaudeAudits(msgs []ctTurn, tenantID, session string) []AuditLog {
	var out []AuditLog
	for _, t := range msgs {
		if t.Role != "assistant" {
			continue
		}
		for _, p := range t.Parts {
			if p.Kind != "tool" {
				continue
			}
			action, ok := claudeAuditTools[p.Tool]
			if !ok {
				continue
			}
			target := p.File
			if target == "" {
				target = p.Info
			}
			out = append(out, AuditLog{
				TenantID: tenantID, ActorKind: "claude", ActorID: session,
				Action: action, Target: target, At: t.TS,
			})
		}
	}
	return out
}

type claudeAuditor struct {
	mgr      *manager
	interval time.Duration
}

func newClaudeAuditor(mgr *manager, iv time.Duration) *claudeAuditor {
	return &claudeAuditor{mgr: mgr, interval: iv}
}

func (a *claudeAuditor) run(ctx context.Context) {
	t := time.NewTicker(a.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.sweep(ctx)
		}
	}
}

func (a *claudeAuditor) sweep(ctx context.Context) {
	tenants, err := a.mgr.store.ListTenants(ctx)
	if err != nil {
		return
	}
	for _, tn := range tenants {
		wss, err := a.mgr.store.ListWorkspaces(ctx, tn.ID)
		if err != nil {
			continue
		}
		for _, ws := range wss {
			rt := a.mgr.runtimeFor(ws, "")
			if rt.State(ctx) != "running" {
				continue
			}
			sessions, err := a.mgr.agentSessions(ctx, rt)
			if err != nil {
				continue
			}
			for _, s := range sessions {
				if s.Kind == "claude" {
					a.auditSession(ctx, ws, rt, s.Name)
				}
			}
		}
	}
}

func (a *claudeAuditor) auditSession(ctx context.Context, ws Workspace, rt Runtime, name string) {
	key := "claude_audit_cursor:" + ws.ID + ":" + name
	cur, _ := a.mgr.store.GetSetting(ctx, key)
	firstTime := cur == ""
	since := cur
	if since == "" {
		since = "0"
	}
	body, err := agentText(ctx, rt, "GET",
		"/sessions/"+url.PathEscape(name)+"/messages?since="+url.QueryEscape(since), nil)
	if err != nil {
		return
	}
	var resp ctResp
	if json.Unmarshal([]byte(body), &resp) != nil {
		return
	}
	// Baseline the cursor on first sight (no retroactive audit); on a transcript reset
	// (compaction/fork) skip this round rather than risk duplicates — just re-baseline.
	if !firstTime && !resp.Reset {
		for _, a0 := range extractClaudeAudits(resp.Messages, ws.TenantID, name) {
			a0.ID = newID()
			if a0.At == "" {
				a0.At = nowTS()
			}
			_ = a.mgr.store.InsertAudit(ctx, a0)
		}
	}
	_ = a.mgr.store.SetSetting(ctx, key, strconv.Itoa(resp.Cursor))
}
