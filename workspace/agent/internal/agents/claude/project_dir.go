package claude

import (
	"os"
	"path/filepath"
	"strings"
)

// claude names a project directory after the cwd it was launched in, replacing every
// character outside [0-9A-Za-z] with '-'. Verified against a live tree:
//
//	/home/dev/repos/agent-fleet          → -home-dev-repos-agent-fleet
//	/home/dev/repos/agent-fleet@wip-s2y  → -home-dev-repos-agent-fleet-wip-s2y
//	/home/dev/.config/agent-fleet/chat-wd → -home-dev--config-agent-fleet-chat-wd
//
// ⚠️ THE ENCODING IS NOT REVERSIBLE and it is not injective: '.', '@', '/' and '_' all
// become '-', so `agent-fleet@wip-x` and `agent-fleet-wip-x` name the same directory. A
// derived name is therefore A GUESS, never the truth — which is why every caller confirms
// the file it derived with a single Lstat and falls back to the full search when that
// misses. What the confirmation makes safe is the positive answer: <sid>.jsonl is unique to
// one session, so finding it there means it IS that session's transcript.
func projectKey(cwd string) string {
	return strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return r
		}
		return '-'
	}, cwd)
}

// guessProjectPath returns ConfigDir()/projects/<projectKey(cwd)>/<rest…> when that path
// exists, and "" otherwise (including when cwd is unknown). One Lstat in place of the
// (1 + number of projects) directory reads filepath.Glob costs — measured at 158 syscalls
// for 38 projects and 1,296 for 318 (ADR 0087 source B).
func guessProjectPath(cwd string, rest ...string) string {
	if cwd == "" {
		return ""
	}
	p := filepath.Join(append([]string{ConfigDir(), "projects", projectKey(cwd)}, rest...)...)
	if _, err := os.Lstat(p); err != nil {
		return ""
	}
	return p
}
