package claude

// ユーザー指示（docs/60）の claude 側 artifact。
//
// claude だけは**合成が要らない**。フリート方針は managed policy
// /etc/claude-code/CLAUDE.md（root 所有・イメージ焼き込み・そもそも dev では書けない）
// として別レイヤに載っており、その下に CLI ネイティブの user memory 層があるので、
// AF はそこへ独立したファイルとして書ける。
//
// 置き場は **$CLAUDE_CONFIG_DIR/CLAUDE.md**（実測 2026-08-13・claude 2.1.229）。
// ⚠️ ~/.claude/CLAUDE.md ではない: AF は CLAUDE_CONFIG_DIR=/var/lib/af/claude を
// 渡しているので、~/.claude/CLAUDE.md に書いても **claude には届かない**
// （opencode も本環境では拾わなかった＝どの kind にも効かない）。docs/60 §60.4-A。
//
// 既定では存在しないファイルなので AF が単独所有できるが、それでも本文全体を
// 上書きせずマーカーで囲む — 利用者が手で書いていた場合にそれを消さないため。

import (
	"os"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mdblock"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// UserInstructionsPath is claude's user-memory file.
func UserInstructionsPath() string {
	return filepath.Join(paths.ClaudeConfigDir(), "CLAUDE.md")
}

// ApplyUserInstructions writes (or removes, when body is empty) the user-notes block.
// Idempotent; writes only when the content changes.
func ApplyUserInstructions(body string) error {
	return setMarkedFile(UserInstructionsPath(), "user-notes", body)
}

// setMarkedFile is the shared read-modify-write for a markdown file agent-fleet
// shares with the user: only the named block changes, and a file that would end up
// empty is removed rather than left as a stray.
func setMarkedFile(path, name, body string) error {
	orig := ""
	if b, err := os.ReadFile(path); err == nil {
		orig = string(b)
	} else if !os.IsNotExist(err) {
		return err
	}
	out := mdblock.Set(orig, name, body)
	if out == orig {
		return nil
	}
	if out == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".af-tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
