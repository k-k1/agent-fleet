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
