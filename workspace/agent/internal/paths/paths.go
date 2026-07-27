// Package paths はホーム配下のパス規約（docs/23 P1-W5、残① Wave A で internal 化）。
// ~/.config/agent-fleet は denylist 配下（ファイルブラウザ非表示）で、fstore の
// 各ストアと資格情報ストアが同居する。package main と internal/session/status の
// 双方から参照される最下層のヘルパ。
package paths

import (
	"os"
	"path/filepath"
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
// registry's session materialize, docs/48 §8) can write the CLI's native config
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

// AgentDataDir is the per-user home volume for larger, persistent agent-fleet data
// (survives container recreate — ~/.local persists). Distinct from AgentConfigDir
// (small JSON state under ~/.config); used for the cleanup archive of removed
// sessions so tidy-up is recoverable.
func AgentDataDir() string {
	return filepath.Join(HomeDir(), ".local", "share", "agent-fleet")
}

// ExePath is the absolute path to this binary, used to build hook/MCP commands
// that resolve in an agent's hook context regardless of PATH (docs/23 残① Wave F
// で main の agentExe / codex の複製を一本化).
func ExePath() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return "/usr/local/bin/workspace-agent"
	}
	return exe
}
