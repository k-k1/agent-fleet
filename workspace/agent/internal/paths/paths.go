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
