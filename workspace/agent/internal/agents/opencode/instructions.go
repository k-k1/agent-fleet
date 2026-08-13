package opencode

// 指示ファイル（docs/60）の opencode 側 artifact。**フリート方針とユーザー指示で
// 置き場が違う**ので、その理由をここに書いておく。
//
//   フリート方針 … ~/.config/opencode/AGENTS.md にマーカー合成。
//                   opencode の global 指示はこのファイル（実測 1.18.18）。
//   ユーザー指示 … AF 専用ファイルを 1 本置き、opencode.json[c] の `instructions`
//                   配列にそのパスを足す（実測: 配列のファイルは実際に読まれる）。
//                   AGENTS.md を触らずに済む＝ docs/60 §60.5-6 の原則どおり。
//
// ⚠️ 実測メモ: バンドルには global 指示として `<home>/.claude/CLAUDE.md` も読む経路が
// あるが、本環境では読まれなかった（docs/60 §60.4-A）。そちらには**依存しない**。
//
// opencode は opencode.jsonc と opencode.json の**両方**を読んでマージするので、
// af はどちらか一方だけを編集する（.jsonc 優先 — mcpreg と同じ規約）。JSON として
// 読めないファイル（.jsonc のコメント等）は**触らない**: 読めない設定を整形し直して
// 利用者の記述を壊すより、配れないことを REST で申告する方がよい。

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mdblock"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// ErrUnreadableConfig is returned when opencode's config exists but is not plain
// JSON. The caller surfaces it; it never rewrites the file.
var ErrUnreadableConfig = errors.New("config_unreadable")

// configNames are the files opencode merges, in the order af prefers to edit them.
var configNames = []string{"opencode.jsonc", "opencode.json"}

// AgentsPath is opencode's global instruction file.
func AgentsPath() string { return filepath.Join(paths.OpencodeConfigDir(), "AGENTS.md") }

// ConfigPath is the config file af edits (the existing one, else .jsonc).
func ConfigPath() string {
	dir := paths.OpencodeConfigDir()
	for _, name := range configNames {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join(dir, configNames[0])
}

// ApplyFleetNotes composes the baked workspace guide into AGENTS.md (see codex's
// twin for why an empty guide is a no-op).
func ApplyFleetNotes(fleet string) error {
	if fleet == "" {
		return nil
	}
	path := AgentsPath()
	orig := ""
	if b, err := os.ReadFile(path); err == nil {
		orig = string(b)
	} else if !os.IsNotExist(err) {
		return err
	}
	out := orig
	if !mdblock.Has(out, "fleet") {
		out = mdblock.StripLegacyPrefix(out, fleet)
	}
	out = mdblock.Set(out, "fleet", fleet)
	if out == orig {
		return nil
	}
	return writeAtomic(path, []byte(out), 0o644)
}

// ApplyUserInstructions writes the AF-owned instruction file and points opencode's
// config at it (or removes both when body is empty).
func ApplyUserInstructions(path, body string) error {
	if body == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return setConfigInstruction(path, false)
	}
	if err := writeAtomic(path, []byte(body), 0o644); err != nil {
		return err
	}
	return setConfigInstruction(path, true)
}

// setConfigInstruction adds/removes one path in the config's `instructions` array,
// touching no other member. A config af cannot parse is left alone.
func setConfigInstruction(instrPath string, want bool) error {
	cfgPath := ConfigPath()
	root := map[string]any{}
	b, rerr := os.ReadFile(cfgPath)
	switch {
	case rerr == nil:
		if json.Unmarshal(b, &root) != nil {
			return ErrUnreadableConfig
		}
	case !os.IsNotExist(rerr):
		return rerr
	case !want:
		return nil // nothing to remove from a file that does not exist
	default:
		// opencode's own writers put this at the top of a file they create.
		root["$schema"] = "https://opencode.ai/config.json"
	}

	cur, _ := root["instructions"].([]any)
	next := make([]any, 0, len(cur)+1)
	found := false
	for _, v := range cur {
		if s, ok := v.(string); ok && s == instrPath {
			found = true
			continue // re-added below when wanted, so order stays stable
		}
		next = append(next, v)
	}
	if want {
		next = append(next, instrPath)
	}
	if found == want && len(next) == len(cur) {
		return nil // already in the desired shape
	}
	if len(next) == 0 {
		delete(root, "instructions")
	} else {
		root["instructions"] = next
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(cfgPath, append(out, '\n'), 0o644)
}

func writeAtomic(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".af-tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
