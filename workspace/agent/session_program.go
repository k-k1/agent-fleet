package main

// CLI（claude / codex）の起動コマンド組み立てと、resume 判定に使う jsonl の
// 所在確認。session.go からの機械的分割（docs/23 P1-W4）。opencode の組み立ては
// internal/agents/opencode へ移設（docs/23 残① Wave D）。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
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
		flags += " --model " + session.ShellQuote(model)
	}
	if label != "" {
		flags += " --name " + session.ShellQuote(label)
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
		return "-c " + session.ShellQuote(val)
	}
	parts := []string{"codex"}
	if codexResumeID != "" {
		parts = append(parts, "resume", session.ShellQuote(codexResumeID))
	}
	parts = append(parts, flags)
	parts = append(parts, hookFlag("UserPromptSubmit", "working"))
	parts = append(parts, hookFlag("Stop", "idle"))
	if model != "" {
		parts = append(parts, "-m", session.ShellQuote(model))
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
