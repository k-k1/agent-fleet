package main

// ホーム配下のパス規約（docs/23 P1-W5）。~/.config/agent-fleet は denylist 配下
// （ファイルブラウザ非表示）で、fstore の各ストアと資格情報ストアが同居する。

import (
	"os"
	"path/filepath"
)

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return os.Getenv("HOME")
}

// agentConfigDir is the root the per-sid file stores (fstore) live under.
func agentConfigDir() string {
	return filepath.Join(homeDir(), ".config", "agent-fleet")
}
