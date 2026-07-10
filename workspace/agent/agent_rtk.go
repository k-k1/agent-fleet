package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
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
//
// Because the entrypoint reseeds the base AGENTS.md / status plugin on every
// container start, the toggle needs a DURABLE preference that survives restarts:
// ~/.config/agent-fleet/rtk.json (absent key ⇒ default ON, matching the historical
// "rtk auto-applied to all agents" behavior). The agent OWNS applying that
// preference to the artifacts — both at startup (reconcileAgentRTK) and live from
// the Console toggle (PUT /agents/rtk) — so the logic lives in one place.

// agentRTKPrefs is the durable per-agent rtk toggle state. Pointers distinguish
// "unset" (⇒ default on) from an explicit false.
type agentRTKPrefs struct {
	Codex    *bool `json:"codex"`
	Opencode *bool `json:"opencode"`
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

// reconcileAgentRTK applies the durable prefs to the on-disk artifacts. Called at
// startup (after the entrypoint reseeded the base files). When rtk is not in the
// image, both are forced off (any stale artifact is removed).
func reconcileAgentRTK() {
	avail := claude.RTKAvailable()
	p := readAgentRTKPrefs()
	opencode.ApplyRTK(avail && prefOnDefault(p.Opencode))
	codex.ApplyRTK(avail && prefOnDefault(p.Codex))
}

func agentRTKBody() map[string]any {
	p := readAgentRTKPrefs()
	return map[string]any{
		"rtk_available": claude.RTKAvailable(),
		"codex_rtk":     prefOnDefault(p.Codex),
		"opencode_rtk":  prefOnDefault(p.Opencode),
	}
}

func handleAgentRTKGet(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, agentRTKBody())
}

type agentRTKReq struct {
	CodexRTK    *bool `json:"codex_rtk"`
	OpencodeRTK *bool `json:"opencode_rtk"`
}

func handleAgentRTKPut(w http.ResponseWriter, r *http.Request) {
	var req agentRTKReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	p := readAgentRTKPrefs()
	if req.CodexRTK != nil {
		p.Codex = req.CodexRTK
	}
	if req.OpencodeRTK != nil {
		p.Opencode = req.OpencodeRTK
	}
	if err := writeAgentRTKPrefs(p); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	reconcileAgentRTK()
	httpx.WriteJSON(w, http.StatusOK, agentRTKBody())
}

// handleAgentRTKGain serves GET /agents/rtk/gain for the Console's WsBar "rtk 効果"
// chip. rtk itself keeps the accounting (compaction bytes/tokens per command) in its
// own history DB under ~/.local/share/rtk; `rtk gain --format json` exposes the daily
// series + cumulative summary. We shell out and pass the JSON through verbatim (plus
// an `available` flag) so the schema stays rtk's, not ours. Best-effort: rtk missing
// or any failure returns a soft body and the Console just hides/blanks the chip.
func handleAgentRTKGain(w http.ResponseWriter, r *http.Request) {
	if !claude.RTKAvailable() {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "rtk", "gain", "--daily", "--format", "json").Output()
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
