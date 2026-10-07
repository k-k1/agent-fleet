package claude

// The claude-side artifact of the user instructions layer (docs/log/60).
//
// claude alone needs no merging: the fleet policy rides on a separate layer as the managed
// policy /etc/claude-code/CLAUDE.md (root-owned, baked into the image, not writable as dev in
// the first place), and the CLI's native user-memory layer sits below it, so AF can write a
// file of its own there.
//
// That file is $CLAUDE_CONFIG_DIR/CLAUDE.md (measured 2026-08-13, claude 2.1.229) — not
// ~/.claude/CLAUDE.md: AF passes CLAUDE_CONFIG_DIR=/var/lib/af/claude, so anything written to
// ~/.claude/CLAUDE.md never reaches claude (opencode did not pick it up in this environment
// either, so it reaches no kind at all). docs/log/60 §60.4-A.
//
// The file does not exist by default, so AF could own it outright; the body is still fenced
// with markers rather than overwritten wholesale, so that anything the user wrote by hand
// survives.

import (
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
	return mdblock.EditFile(path, 0o644, 0o755, true, func(s string) (string, error) { return mdblock.Set(s, name, body), nil })
}

// setMarkedFileSafe is setMarkedFile for a block that must never eat the member's text: a file
// with damaged markers is left alone and reported.
func setMarkedFileSafe(path, name, body string) error {
	return mdblock.EditFile(path, 0o644, 0o755, true, func(s string) (string, error) { return mdblock.SetSafe(s, name, body) })
}

// ApplyMemoryGuide writes (or removes, when body is empty) the memory-guide block (ADR 0108
// decision 5) in the same file, apart from the user-notes block.
func ApplyMemoryGuide(body string) error {
	return setMarkedFileSafe(UserInstructionsPath(), "memory-guide", body)
}
