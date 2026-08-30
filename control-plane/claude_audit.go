package main

import (
	"context"
	"encoding/json"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Claude self-operation audit (docs/log/20 M5, A-第2段). claude runs untrusted inside the
// container and edits files / runs Bash directly; those don't flow through the CP proxy
// (M1), so they'd be invisible. The isolation model blocks Agent→CP, so instead of a
// push from claude's PreToolUse hook the CP PULLS: a background sweeper reads each
// running claude session's transcript (CP→Agent, the normal direction) and records the
// Write/Edit/Bash tool_use into the audit ledger with actor_kind=claude, attributed to
// the workspace's tenant. A per-session cursor (deployment_setting kv) makes it
// incremental; the first sighting only baselines the cursor so pre-existing history
// isn't audited retroactively. Opt-in: AF_CLAUDE_AUDIT_INTERVAL=0 (default) disables it.

// claudeAuditTools maps a claude tool name to the audit action it records. Read/Grep/
// etc. are intentionally absent — M5 records change/exec operations only (docs/log/20 §E.1).
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

const claudeAuditCursorPrefix = "claude_audit_cursor:"

func (a *claudeAuditor) sweep(ctx context.Context) {
	tenants, err := a.mgr.store.ListTenants(ctx)
	if err != nil {
		return
	}
	liveWS := map[string]bool{}  // 現存する全ワークスペース
	checked := map[string]bool{} // セッション一覧まで取得できた running WS
	liveKey := map[string]bool{} // 現存 claude セッションのカーソル key
	complete := true             // 部分失敗があれば掃除は見送る(誤削除防止)
	for _, tn := range tenants {
		wss, err := a.mgr.store.ListWorkspaces(ctx, tn.ID)
		if err != nil {
			complete = false
			continue
		}
		for _, ws := range wss {
			liveWS[ws.ID] = true
			rt := a.mgr.runtimeFor(ws, "")
			if rt.State(ctx) != "running" {
				continue
			}
			sessions, err := a.mgr.agentSessions(ctx, rt)
			if err != nil {
				continue
			}
			checked[ws.ID] = true
			for _, s := range sessions {
				if s.Kind == "claude" {
					liveKey[claudeAuditCursorPrefix+ws.ID+":"+s.Name] = true
					a.auditSession(ctx, ws, rt, s.Name)
				}
			}
		}
	}
	if complete {
		a.cleanupCursors(ctx, liveWS, checked, liveKey)
	}
}

// cleanupCursors drops per-session cursor keys that can no longer fire: the
// workspace was deleted, or the (successfully enumerated, running) workspace no
// longer has that session. 停止中WSのセッションは列挙できないため据え置く。
func (a *claudeAuditor) cleanupCursors(ctx context.Context, liveWS, checked, liveKey map[string]bool) {
	keys, err := a.mgr.store.ListSettingKeys(ctx, claudeAuditCursorPrefix)
	if err != nil {
		return
	}
	for _, key := range keys {
		rest := strings.TrimPrefix(key, claudeAuditCursorPrefix)
		wsID, _, ok := strings.Cut(rest, ":")
		if !ok {
			continue
		}
		if liveWS[wsID] && (!checked[wsID] || liveKey[key]) {
			continue
		}
		if err := a.mgr.store.DeleteSetting(ctx, key); err != nil {
			log.Printf("claude-audit: cursor cleanup failed key=%s: %v", key, err)
		}
	}
}

func (a *claudeAuditor) auditSession(ctx context.Context, ws Workspace, rt Runtime, name string) {
	key := claudeAuditCursorPrefix + ws.ID + ":" + name
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
			if err := a.mgr.store.InsertAudit(ctx, a0); err != nil {
				// カーソルを進めると監査行がサイレント欠落する。据え置いて次回再試行
				// (途中まで入った行は重複し得るが、欠落よりは重複を選ぶ)。
				log.Printf("claude-audit: insert failed ws=%s session=%s: %v", ws.ID, name, err)
				return
			}
		}
	}
	if err := a.mgr.store.SetSetting(ctx, key, strconv.Itoa(resp.Cursor)); err != nil {
		log.Printf("claude-audit: cursor save failed ws=%s session=%s: %v", ws.ID, name, err)
	}
}
