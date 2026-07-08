package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// rtk (token-saving CLI proxy) for the non-claude agents. claude routes Bash
// through rtk via its settings.json PreToolUse hook (see claude_settings.go);
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

const codexRTKMarkerStart = "<!-- agent-fleet:rtk -->"
const codexRTKMarkerEnd = "<!-- /agent-fleet:rtk -->"

// codexRTKBlock is the instruction appended to codex's AGENTS.md when rtk is on.
// Kept terse; codex reads AGENTS.md at session start.
const codexRTKBlock = "## rtk (token saver) — prefer it for shell commands\n" +
	"`rtk` is a CLI proxy that compacts command output to save context tokens. Prefix\n" +
	"read / inspect / build commands with it — same command, smaller output:\n" +
	"`rtk git status`, `rtk ls`, `rtk grep ...`, `rtk cargo test`, `rtk npm run build`.\n" +
	"Skip it only when you need the raw, unfiltered stream; `rtk proxy <cmd>` runs a\n" +
	"command without filtering.\n"

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

// codexHome mirrors codex's own resolution: $CODEX_HOME, else ~/.codex (where the
// entrypoint seeds AGENTS.md).
func codexHome() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	return filepath.Join(homeDir(), ".codex")
}

func codexAgentsPath() string { return filepath.Join(codexHome(), "AGENTS.md") }

// opencode 側の適用（rtk.ts プラグインの seed/remove）は opencode.ApplyRTK
// （internal/agents/opencode、docs/23 残① Wave D）。

// stripMarkedBlock removes the region from start..end (inclusive) and rejoins. A
// missing end marker (malformed file) drops everything from start onward.
func stripMarkedBlock(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return s
	}
	rest := s[i+len(start):]
	k := strings.Index(rest, end)
	if k < 0 {
		return strings.TrimRight(s[:i], "\n") + "\n"
	}
	tail := rest[k+len(end):]
	head := strings.TrimRight(s[:i], "\n")
	tail = strings.TrimLeft(tail, "\n")
	if head == "" {
		return tail
	}
	if tail == "" {
		return head + "\n"
	}
	return head + "\n\n" + tail
}

// applyCodexRTK appends (on) or removes (off) the marked rtk block in AGENTS.md.
// Idempotent: any prior block is stripped first. Writes only when changed.
func applyCodexRTK(on bool) {
	path := codexAgentsPath()
	orig := ""
	if b, err := os.ReadFile(path); err == nil {
		orig = string(b)
	}
	out := stripMarkedBlock(orig, codexRTKMarkerStart, codexRTKMarkerEnd)
	if on {
		block := codexRTKMarkerStart + "\n" + codexRTKBlock + codexRTKMarkerEnd + "\n"
		if out == "" {
			out = block
		} else {
			out = strings.TrimRight(out, "\n") + "\n\n" + block
		}
	}
	if out == orig || out == "" {
		return // no change, or nothing to write (no base file & rtk off)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".af-tmp"
	if os.WriteFile(tmp, []byte(out), 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

// reconcileAgentRTK applies the durable prefs to the on-disk artifacts. Called at
// startup (after the entrypoint reseeded the base files). When rtk is not in the
// image, both are forced off (any stale artifact is removed).
func reconcileAgentRTK() {
	avail := rtkAvailable()
	p := readAgentRTKPrefs()
	opencode.ApplyRTK(avail && prefOnDefault(p.Opencode))
	applyCodexRTK(avail && prefOnDefault(p.Codex))
}

func agentRTKBody() map[string]any {
	p := readAgentRTKPrefs()
	return map[string]any{
		"rtk_available": rtkAvailable(),
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
