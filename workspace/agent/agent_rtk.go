package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// rtk (token-saving CLI proxy) for the non-claude agents. claude routes Bash
// through rtk via its settings.json PreToolUse hook (see internal/agents/claude);
// codex and opencode have no such hook, so their on/off state is a different
// artifact entirely:
//
//   opencode … presence of the rtk plugin file ~/.config/opencode/plugin/rtk.ts
//              (intercepts bash/shell tool calls → `rtk rewrite`). Transparent.
//   codex    … a marked rtk-usage block appended to ~/.codex/AGENTS.md (codex has
//              no command-rewrite hook, so it is instruction-based / best-effort).
//   copilot  … a user-scope preToolUse hook file $COPILOT_HOME/hooks/rtk.json
//              (`rtk hook copilot` rewrites the shell tool's command via modifiedArgs).
//              Transparent & deterministic like claude/opencode (see copilot/rtk.go).
//
// Because the entrypoint reseeds the base AGENTS.md / status plugin on every
// container start, the toggle needs a DURABLE preference that survives restarts:
// ~/.config/agent-fleet/rtk.json (absent key ⇒ default ON, matching the historical
// "rtk auto-applied to all agents" behavior). The agent OWNS applying that
// preference to the artifacts — both at startup (reconcileAgentRTK) and live from
// the Console toggle (PUT /agents/rtk) — so the logic lives in one place.

// agentRTKMu serializes the rtk.json read-modify-write (PUT is per-field merge, so
// two concurrent PUTs would otherwise lose one side's toggle).
var agentRTKMu sync.Mutex

// agentRTKPrefs is the durable per-agent rtk toggle state. Pointers distinguish
// "unset" (⇒ default on) from an explicit false.
type agentRTKPrefs struct {
	Codex    *bool `json:"codex"`
	Opencode *bool `json:"opencode"`
	Agy      *bool `json:"agy"`
	Copilot  *bool `json:"copilot"`
}

func agentRTKPrefsPath() string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", "rtk.json")
}

func readAgentRTKPrefs() agentRTKPrefs {
	var p agentRTKPrefs
	if b, err := os.ReadFile(agentRTKPrefsPath()); err == nil {
		_ = json.Unmarshal(b, &p)
	}
	return p
}

func writeAgentRTKPrefs(p agentRTKPrefs) error {
	path := agentRTKPrefsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func prefOnDefault(p *bool) bool {
	if p == nil {
		return true
	}
	return *p
}

// 各エージェント側の適用 artifact は縦割りパッケージへ移設済み: opencode は
// opencode.ApplyRTK（rtk.ts プラグインの seed/remove、docs/23 残① Wave D）、codex
// は codex.ApplyRTK（AGENTS.md のマーカーブロック、同 Wave E）。

// reconcileAgentRTK applies the durable prefs to the on-disk artifacts. When rtk is
// not in the image, all are forced off (any stale artifact is removed).
//
// codex / agy の artifact は AGENTS.md のブロックで、同じファイルへユーザー指示と
// フリート方針も書かれる。だから書き込みは instrMu で直列化し、順序も
// fleet → user-notes → rtk に固定する（agent_instructions.go）。
func reconcileAgentRTK() {
	instrMu.Lock()
	defer instrMu.Unlock()
	applyRTKLocked()
}

// applyRTKLocked は instrMu を保持した状態で呼ぶ本体。
func applyRTKLocked() {
	avail := claude.RTKAvailable()
	p := readAgentRTKPrefs()
	opencode.ApplyRTK(avail && prefOnDefault(p.Opencode))
	codex.ApplyRTK(avail && prefOnDefault(p.Codex))
	agy.ApplyRTK(avail && prefOnDefault(p.Agy))
	copilot.ApplyRTK(avail && prefOnDefault(p.Copilot))
}

func agentRTKBody() map[string]any {
	p := readAgentRTKPrefs()
	return map[string]any{
		"rtk_available": claude.RTKAvailable(),
		"codex_rtk":     prefOnDefault(p.Codex),
		"opencode_rtk":  prefOnDefault(p.Opencode),
		"agy_rtk":       prefOnDefault(p.Agy),
		"copilot_rtk":   prefOnDefault(p.Copilot),
	}
}

func handleAgentRTKGet(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, agentRTKBody())
}

type agentRTKReq struct {
	CodexRTK    *bool `json:"codex_rtk"`
	OpencodeRTK *bool `json:"opencode_rtk"`
	AgyRTK      *bool `json:"agy_rtk"`
	CopilotRTK  *bool `json:"copilot_rtk"`
}

func handleAgentRTKPut(w http.ResponseWriter, r *http.Request) {
	var req agentRTKReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	agentRTKMu.Lock()
	defer agentRTKMu.Unlock()
	p := readAgentRTKPrefs()
	if req.CodexRTK != nil {
		p.Codex = req.CodexRTK
	}
	if req.OpencodeRTK != nil {
		p.Opencode = req.OpencodeRTK
	}
	if req.AgyRTK != nil {
		p.Agy = req.AgyRTK
	}
	if req.CopilotRTK != nil {
		p.Copilot = req.CopilotRTK
	}
	if err := writeAgentRTKPrefs(p); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	reconcileAgentRTK()
	httpx.WriteJSON(w, http.StatusOK, agentRTKBody())
}

// handleAgentRTKGain serves GET /agents/rtk/gain for the Console's usage-tab "rtk 効果"
// card. rtk itself keeps the accounting (compaction bytes/tokens per command) in its
// own history DB under ~/.local/share/rtk; `rtk gain --all --format json` exposes the
// daily / weekly / monthly series + cumulative summary. We shell out and pass the JSON
// through verbatim (plus an `available` flag) so the schema stays rtk's, not ours.
// (The per-command "By Command" table and quota estimate are text-only in rtk 0.44 —
// not exposed here; parsing its text output would be a version-fragile contract.)
// Best-effort: rtk missing or any failure returns a soft body and the Console just
// hides the card.
func handleAgentRTKGain(w http.ResponseWriter, r *http.Request) {
	if !claude.RTKAvailable() {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "rtk", "gain", "--all", "--format", "json").Output()
	if err != nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"available": true, "error": err.Error()})
		return
	}
	var body map[string]any
	if err := json.Unmarshal(out, &body); err != nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"available": true, "error": "parse_failed"})
		return
	}
	body["available"] = true
	httpx.WriteJSON(w, http.StatusOK, body)
}
