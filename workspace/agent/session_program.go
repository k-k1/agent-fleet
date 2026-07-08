package main

// CLI（claude / opencode / codex）の起動コマンド組み立てと、resume 判定に使う
// jsonl の所在確認。session.go からの機械的分割（docs/23 P1-W4）。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// buildSessionProgram returns the shell command tmux should run for a session.
// AGENT_SESSION_CMD overrides claude entirely (e.g. "bash") for plumbing tests.
// Otherwise it resumes when a session jsonl already exists, else starts new.
// label, when non-empty, becomes claude's --name (display name shown in the
// Remote Control picker and terminal title), e.g. "[AF] agent-fleet @0627-2115".
func buildSessionProgram(sid, model, label, forkFrom string) string {
	if override := os.Getenv("AGENT_SESSION_CMD"); override != "" {
		return override
	}
	flags := envOr("AGENT_CLAUDE_FLAGS", "--dangerously-skip-permissions")
	if model != "" {
		flags += " --model " + shellQuote(model)
	}
	if label != "" {
		flags += " --name " + shellQuote(label)
	}
	if sessionJSONLExists(sid) {
		// Already materialized (normal session, or a fork after its first launch):
		// resume our own jsonl. ForkFrom is intentionally ignored here so a restart
		// never re-copies the source.
		return fmt.Sprintf("claude --resume %s %s", sid, flags)
	}
	if forkFrom != "" {
		// First launch of a fork: copy the source conversation into OUR sid via the
		// official --fork-session, pinning the new id with --session-id so it lands
		// exactly on our deterministic jsonl (verified: --session-id sets the fork's
		// id). The source jsonl is left untouched.
		return fmt.Sprintf("claude --resume %s --fork-session --session-id %s %s", forkFrom, sid, flags)
	}
	return fmt.Sprintf("claude --session-id %s %s", sid, flags)
}

// buildOpencodeProgram returns the tmux program for an opencode session. opencode
// keeps its sessions in a local SQLite db (~/.local/share/opencode) and --continue
// resumes the most recent session for the current project, while safely starting a
// new one when none exists — so we always pass it (first launch = fresh, relaunch =
// continue). Auth is the user's own `opencode auth login` (persisted in home), so
// there's no token to inject. Caveat: multiple opencode slots in the SAME dir share
// --continue's "most recent" target.
func buildOpencodeProgram(model string, envs []string, ocid string) string {
	// Prefix env assignments onto the command so the opencode process actually
	// receives them (NAME='value' … opencode). Values are shell-quoted; names are
	// trusted (our sid + validated ALL_CAPS provider env names).
	prefix := ""
	for _, kv := range envs {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		prefix += kv[:i] + "=" + shellQuote(kv[i+1:]) + " "
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
		parts = append(parts, "--session", shellQuote(ocid))
	}
	if model != "" {
		// opencode expects provider/model (e.g. anthropic/claude-...); passed through
		// verbatim. The Console only sends this for opencode when explicitly chosen.
		parts = append(parts, "--model", shellQuote(model))
	}
	return prefix + strings.Join(parts, " ")
}

// buildCodexProgram returns the tmux program for a codex session. codex owns its
// auth (~/.codex/auth.json) so no token is injected. It generates its OWN session
// id (no --session-id flag), so we:
//   - inject status hooks via -c, baking OUR slot sid into the hook command so the
//     reported working/idle state is keyed by the slot (codex's hook JSON carries
//     codex's own session_id, which the helper records for resume);
//   - resume exactly THIS slot's codex session when we've captured its id
//     (codexResumeID), else launch plain codex (a fresh session) — mirroring the
//     opencode per-slot model so two codex slots in one dir don't collide.
//
// The bypass flags make codex run unattended like claude's --dangerously-skip-
// permissions: the container IS the sandbox, and we author the injected hooks so
// hook-trust is bypassed too (otherwise the status hooks wouldn't fire).
func buildCodexProgram(model, slotSid, codexResumeID string) string {
	if override := os.Getenv("AGENT_CODEX_CMD"); override != "" {
		return override
	}
	flags := envOr("AGENT_CODEX_FLAGS", "--dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust")
	exe := agentExe()
	// A hook entry as a TOML inline array-of-tables value for `-c hooks.<event>=…`.
	// The command bakes in our slot sid + the "codex" marker so the status helper
	// keys by the slot and captures codex's own session id from the hook's stdin.
	hookFlag := func(event, state string) string {
		cmd := fmt.Sprintf("%s session-status %s %s codex", exe, state, slotSid)
		// codex uses claude's hook schema: hooks.<Event> is an array of entries that
		// each hold a NESTED "hooks" list of {type,command}. A flat [{type,command}]
		// parses without error but never fires, so the nesting is required.
		val := fmt.Sprintf(`hooks.%s=[{hooks=[{type="command",command=%s}]}]`, event, tomlString(cmd))
		return "-c " + shellQuote(val)
	}
	parts := []string{"codex"}
	if codexResumeID != "" {
		parts = append(parts, "resume", shellQuote(codexResumeID))
	}
	parts = append(parts, flags)
	parts = append(parts, hookFlag("UserPromptSubmit", "working"))
	parts = append(parts, hookFlag("Stop", "idle"))
	if model != "" {
		parts = append(parts, "-m", shellQuote(model))
	}
	return strings.Join(parts, " ")
}

// tomlString renders s as a TOML basic string (double-quoted, backslash/quote
// escaped) for embedding in a `-c key=value` override.
func tomlString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// jsonlPaths returns the conversation log file(s) for sid. claude stores them
// under claudeConfigDir()/projects/<project>/<sid>.jsonl (CLAUDE_CONFIG_DIR when
// set, P3-5 段2) — NOT a hardcoded ~/.claude.
func jsonlPaths(sid string) []string {
	m, _ := filepath.Glob(filepath.Join(claudeConfigDir(), "projects", "*", sid+".jsonl"))
	return m
}

// sessionJSONLExists reports whether a conversation log for sid is on disk. When
// it exists buildSessionProgram uses --resume; otherwise --session-id starts new.
// A wrong answer here makes claude exit ("Session ID is already in use").
func sessionJSONLExists(sid string) bool { return len(jsonlPaths(sid)) > 0 }

// jsonlResumable reports whether sid's log holds an actual conversation (a user or
// assistant turn) — not just bookkeeping lines (Remote Control "bridge-session",
// a lone summary, …). claude --resume fails ("No conversation found") on a stub
// log even though the file exists, so we gate resume on real content.
func jsonlResumable(sid string) bool {
	for _, p := range jsonlPaths(sid) {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := string(b)
		if strings.Contains(s, `"type":"user"`) || strings.Contains(s, `"type":"assistant"`) {
			return true
		}
	}
	return false
}
