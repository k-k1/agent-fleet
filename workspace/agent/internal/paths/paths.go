// Package paths はホーム配下のパス規約（docs/log/23 P1-W5、残① Wave A で internal 化）。
// ~/.config/agent-fleet は denylist 配下（ファイルブラウザ非表示）で、fstore の
// 各ストアと資格情報ストアが同居する。package main と internal/session/status の
// 双方から参照される最下層のヘルパ。
package paths

import (
	"os"
	"path/filepath"
	"strings"
)

func HomeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return os.Getenv("HOME")
}

// AgentConfigDir is the root the per-sid file stores (fstore) live under.
func AgentConfigDir() string {
	return filepath.Join(HomeDir(), ".config", "agent-fleet")
}

// ClaudeConfigDir resolves where the claude CLI reads/writes its state
// (settings.json, .claude.json, projects/*.jsonl). P3-5 段2 relocates that tree out
// of home via CLAUDE_CONFIG_DIR; unset means the classic ~/.claude.
//
// It lives here rather than in internal/agents/claude so a lower layer (the MCP
// registry's session materialize, docs/log/48 §8) can write the CLI's native config
// without importing an agent package — one resolver, no drifting copy.
func ClaudeConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(HomeDir(), ".claude")
}

// CodexHome mirrors codex's own resolution: $CODEX_HOME, else ~/.codex (where the
// entrypoint seeds AGENTS.md and codex keeps config.toml). Same rationale as
// ClaudeConfigDir.
func CodexHome() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	return filepath.Join(HomeDir(), ".codex")
}

// OpencodeConfigDir is opencode's global config root (opencode.jsonc, plugin/,
// AGENTS.md). opencode itself resolves it through XDG_CONFIG_HOME, but the workspace
// never sets that variable and every other af writer into this tree (the rtk plugin,
// the entrypoint's permission block and AGENTS.md seed) spells it from HOME — so this
// stays HOME-based, and af's writes all land in the same directory.
func OpencodeConfigDir() string {
	return filepath.Join(HomeDir(), ".config", "opencode")
}

// CopilotHome is copilot's state root ($COPILOT_HOME, else ~/.copilot): config.json,
// mcp-config.json, session-state/.
func CopilotHome() string {
	if d := os.Getenv("COPILOT_HOME"); d != "" {
		return d
	}
	return filepath.Join(HomeDir(), ".copilot")
}

// FleetNotesPath is the workspace guide baked into the image
// (`workspace/workspace-notes.md` — the fleet layer of docs/log/60). The agent composes
// it into each CLI's global instruction file at startup; the env override exists so
// tests can point at a fixture instead of the image copy.
func FleetNotesPath() string {
	if p := os.Getenv("AF_WORKSPACE_NOTES"); p != "" {
		return p
	}
	return "/usr/local/share/agent-fleet/workspace-notes.md"
}

// CursorHome is cursor's state root (~/.cursor): mcp.json, cli-config.json, projects/.
func CursorHome() string { return filepath.Join(HomeDir(), ".cursor") }

// KiroHome is kiro's config/session root (~/.kiro): settings/, agents/, sessions/cli/.
func KiroHome() string { return filepath.Join(HomeDir(), ".kiro") }

// GeminiHome is the tree agy inherits from its gemini-cli lineage (~/.gemini, hardcoded
// off $HOME): antigravity-cli/ for state and config/ for settings and mcp_config.json.
func GeminiHome() string { return filepath.Join(HomeDir(), ".gemini") }

// AgentDataDir is the per-user home volume for larger, persistent agent-fleet data
// (survives container recreate — ~/.local persists). Distinct from AgentConfigDir
// (small JSON state under ~/.config); used for the cleanup archive of removed
// sessions so tidy-up is recoverable.
func AgentDataDir() string {
	return filepath.Join(HomeDir(), ".local", "share", "agent-fleet")
}

// ExePath is the absolute path to this binary, used to build hook/MCP commands
// that resolve in an agent's hook context regardless of PATH (docs/log/23 残① Wave F
// で main の agentExe / codex の複製を一本化).
func ExePath() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return InstalledExePath()
	}
	return exe
}

// InstalledExePath is where the workspace image puts the agent — the one path that
// outlives any single build of it. The env override exists for tests and for a host
// install that isn't in the image location.
func InstalledExePath() string {
	if p := os.Getenv("AF_AGENT_INSTALLED_BIN"); p != "" {
		return p
	}
	return "/usr/local/bin/workspace-agent"
}

// ConfigExePath is the agent path to write into ANOTHER program's persistent config
// (claude's statusLine and hooks, a CLI's MCP server command). Usually that is this
// binary — but a build running from a volatile directory (a dev build in /tmp, an
// e2e/smoke copy, anything under the scratch disk) must not pin ITS path there: the
// config outlives the binary, and once the file is gone the CLI just fails to run the
// command, silently. Measured: a smoke build in /tmp wrote `/tmp/af-agent statusline`
// into the shared settings.json, was deleted two minutes later, and the usage capture
// stopped dead — the chip then displayed a fabricated 0% for six hours. So when this
// binary is ephemeral, persist the installed one instead (which resolves its own
// state from the session's env exactly the same way).
func ConfigExePath() string {
	exe := ExePath()
	if !volatilePath(exe) {
		return exe
	}
	if fi, err := os.Stat(InstalledExePath()); err == nil && !fi.IsDir() {
		return InstalledExePath()
	}
	return exe // nothing installed (host/native dev): our own path is all there is
}

// ExeUnusable reports whether an agent path already recorded in a config can no
// longer be relied on — it is gone, or it lives where it will be wiped out from
// under the config. Callers repoint such a command at ConfigExePath.
func ExeUnusable(p string) bool {
	if p == "" {
		return true
	}
	if volatilePath(p) {
		return true
	}
	_, err := os.Stat(p)
	return err != nil
}

// volatilePath reports whether p sits under a directory whose contents do not
// survive: the temp dirs, and the workspace scratch disk (wiped on every stop).
func volatilePath(p string) bool {
	roots := []string{os.TempDir(), "/tmp", "/var/tmp"}
	if s := os.Getenv("AF_WS_SCRATCH"); s != "" {
		roots = append(roots, s)
	}
	for _, r := range roots {
		if r == "" || r == "/" {
			continue
		}
		if strings.HasPrefix(p, strings.TrimSuffix(r, "/")+"/") {
			return true
		}
	}
	return false
}
