package copilot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// copilot の config.json（$COPILOT_HOME/config.json）への trustedFolders 事前追記。
// 実測（v1.0.73）: TUI は未信頼フォルダで "Confirm folder trust" ダイアログを出して
// 止まり、「Yes, and remember」で config.json の trustedFolders[] に保存される —
// 事前追記で同じ状態を作ればダイアログはスキップされる。ファイルは先頭に //
// コメント行を持つ JSONC 風（"This file is managed automatically."）なので、
// コメント行を保存して JSON 本体だけを読み書きする。

// readConfig parses config.json, returning the leading comment lines verbatim
// and the JSON object (empty map when absent/corrupt).
func readConfig() (comments []string, m map[string]any) {
	m = map[string]any{}
	b, err := os.ReadFile(configPath())
	if err != nil {
		return nil, m
	}
	s := string(b)
	for {
		line, rest, found := strings.Cut(s, "\n")
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			comments = append(comments, line)
			if !found {
				s = ""
				break
			}
			s = rest
			continue
		}
		break
	}
	_ = json.Unmarshal([]byte(s), &m)
	return comments, m
}

// writeConfig persists m atomically, restoring the original comment header.
// copilot 自身も同じファイルを書くため、未知キーは readConfig の map 経由で保存
// される。
func writeConfig(comments []string, m map[string]any) {
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	var sb strings.Builder
	for _, c := range comments {
		sb.WriteString(c)
		sb.WriteString("\n")
	}
	sb.Write(b)
	sb.WriteString("\n")
	tmp := p + ".af-tmp"
	if os.WriteFile(tmp, []byte(sb.String()), 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

// EnsureFolderTrusted pre-accepts copilot's folder-trust gate for dir — exactly
// what copilot writes when the user answers "Yes, and remember this folder"
// (実測). Best-effort and idempotent; called on every launch（一回きりの固定は
// 後で剥がれる — agy 3a2c9df の教訓）。
func EnsureFolderTrusted(dir string) {
	if dir == "" {
		return
	}
	comments, m := readConfig()
	list, _ := m["trustedFolders"].([]any)
	for _, v := range list {
		if s, ok := v.(string); ok && s == dir {
			return
		}
	}
	m["trustedFolders"] = append(list, dir)
	writeConfig(comments, m)
}
