package codex

import (
	"fmt"
	"os"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// envOr is a copy of the identically named helper in package main (too small to be worth
// sharing).
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// buildProgram returns the tmux program for a codex session. codex owns its
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
func buildProgram(model, effort, slotSid, codexResumeID, forkFrom string) string {
	if override := os.Getenv("AGENT_CODEX_CMD"); override != "" {
		return override
	}
	flags := envOr("AGENT_CODEX_FLAGS", "--dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust")
	exe := paths.ExePath()
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
	if addr := os.Getenv("AF_CODEX_APP_SERVER_ADDR"); addr != "" {
		// Global options must precede resume/fork. The TUI remains interactive; only
		// its backend moves behind the local app-server observed by Agent Fleet.
		parts = append(parts, "--remote", session.ShellQuote(addr))
	}
	switch {
	case codexResumeID != "":
		parts = append(parts, "resume", session.ShellQuote(codexResumeID))
	case forkFrom != "":
		// First launch of a forked slot: copy forkFrom's conversation into a new
		// session and diverge (the codex analog of claude's --fork-session).
		parts = append(parts, "fork", session.ShellQuote(forkFrom))
	}
	parts = append(parts, flags)
	// request_user_input (codex's AskUserQuestion) is gated OFF in the TUI's Default
	// mode — the model is told "the tool is unavailable in Default mode" and answers
	// itself instead of asking. Without this the "has a question" chip / notification on the CLI
	// route can never fire: WireLive's HasPendingQuestion probe looks for a pending
	// request_user_input call in the rollout, and none is ever recorded. Plan mode
	// enables the tool on its own, so only Default mode needed the opt-in — which is
	// why this went unnoticed. Verified on codex 0.144.3 (the pinned build) and 0.144.5:
	// without the flag the tool is refused in Default mode, with it the question dialog
	// renders and the rollout records the function_call the probe keys on. Mirrors the
	// managed driver, which passes the same feature per-thread (see threadFeatures).
	parts = append(parts, "-c", session.ShellQuote("features.default_mode_request_user_input=true"))
	parts = append(parts, hookFlag("UserPromptSubmit", "working"))
	parts = append(parts, hookFlag("Stop", "idle"))
	if model != "" {
		parts = append(parts, "-m", session.ShellQuote(model))
	}
	if effort != "" {
		val := "model_reasoning_effort=" + tomlString(effort)
		parts = append(parts, "-c", session.ShellQuote(val))
	}
	return strings.Join(parts, " ")
}

// tomlString renders s as a TOML basic string (double-quoted, backslash/quote
// escaped) for embedding in a `-c key=value` override.
func tomlString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}
