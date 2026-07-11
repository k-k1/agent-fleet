package opencode

import (
	"os"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// envOr は package main の同名ヘルパの複製（極小のため共有せず重複を許容）。
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// buildProgram returns the tmux program for an opencode session. opencode
// keeps its sessions in a local SQLite db (~/.local/share/opencode) and --continue
// resumes the most recent session for the current project, while safely starting a
// new one when none exists — so we always pass it (first launch = fresh, relaunch =
// continue). Auth is the user's own `opencode auth login` (persisted in home), so
// there's no token to inject. Caveat: multiple opencode slots in the SAME dir share
// --continue's "most recent" target.
func buildProgram(model string, envs []string, ocid string, fork bool) string {
	// Prefix env assignments onto the command so the opencode process actually
	// receives them (NAME='value' … opencode). Values are shell-quoted; names are
	// trusted (our sid + validated ALL_CAPS provider env names).
	prefix := ""
	for _, kv := range envs {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		prefix += kv[:i] + "=" + session.ShellQuote(kv[i+1:]) + " "
	}
	parts := []string{"opencode"}
	// Run unattended like claude (--dangerously-skip-permissions) and codex
	// (--dangerously-bypass-…): the container IS the sandbox, so auto-approve every
	// permission prompt (external-dir access, edits, bash) instead of stalling the TUI on
	// an approval the Console user can't answer from chat. --auto approves anything not
	// explicitly denied. Overridable via AGENT_OPENCODE_FLAGS (set to alternate flags).
	parts = append(parts, envOr("AGENT_OPENCODE_FLAGS", "--auto"))
	// Per-slot session: when we've captured this slot's opencode session id (the
	// plugin records it on session.created, keyed by AF_SESSION_SID), resume exactly
	// THAT session. Otherwise launch plain opencode — the TUI creates a fresh session
	// on first message, distinct from other slots. We deliberately do NOT use
	// --continue: it resumes the most-recent session in the project, so two slots in
	// the same dir would collide on one shared conversation.
	if ocid != "" {
		parts = append(parts, "--session", session.ShellQuote(ocid))
		if fork {
			// Fork launch: copy ocid's conversation into a NEW session and diverge
			// (the opencode analog of claude's --fork-session). First launch only —
			// once the fork exists the caller resumes it without --fork.
			parts = append(parts, "--fork")
		}
	}
	if model != "" {
		// opencode expects provider/model (e.g. anthropic/claude-...); passed through
		// verbatim. The Console only sends this for opencode when explicitly chosen.
		parts = append(parts, "--model", session.ShellQuote(model))
	}
	return prefix + strings.Join(parts, " ")
}
