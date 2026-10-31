package opencode

import (
	"encoding/json"
	"os"
	"strings"

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

// buildProgram returns the tmux program for an opencode session. opencode
// keeps its sessions in a local SQLite db (~/.local/share/opencode) and --continue
// resumes the most recent session for the current project, while safely starting a
// new one when none exists — so we always pass it (first launch = fresh, relaunch =
// continue). Auth is the user's own `opencode auth login` (persisted in home), so
// there's no token to inject. Caveat: multiple opencode slots in the SAME dir share
// --continue's "most recent" target.
func buildProgram(model, mode string, ocid string, fork bool) string {
	return buildProgramFor(CurrentLaunch(), model, mode, ocid, fork)
}

// buildProgramFor is buildProgram against one reading of the installed opencode.
func buildProgramFor(spec LaunchSpec, model, mode string, ocid string, fork bool) string {
	if spec.Major() >= 2 {
		return buildProgramV2(spec, model, mode, ocid, fork)
	}
	// Env (AF_SESSION_SID + provider API keys) is carried on LaunchPlan.Env and
	// injected by tmux `new-session -e`: prefixing NAME='value' onto the command
	// put the keys into /proc/*/cmdline and pane_start_command in plaintext.
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
	if mode == "plan" || mode == "normal" {
		agent := "build"
		if mode == "plan" {
			agent = "plan"
		}
		parts = append(parts, "--agent", agent)
	}
	return strings.Join(parts, " ")
}

// buildProgramV2 is the --standalone launch line for opencode 2: the fallback when af's own serve
// cannot be had and the AF_OPENCODE_TUI_STANDALONE=1 switch (shared.go is the default, one serve
// for every Terminal session). Measured on 2.0.28: the TUI takes only
// --standalone, --server, --auto, --continue, --session and --prompt, so --model, --agent and
// --fork (v1 TUI flags, `opencode run` only on v2) are replaced by what v2 does support.
//
//   - --standalone: a private server per TUI. A bare v2 `opencode` instead joins one per-user
//     background service that inherited the environment of whichever session started it, so
//     AF_SESSION_SID (status plugin, slot id capture), the session-scoped engine token and the
//     provider keys would all be the starter's. docs/log/119 holds the RSS comparison with
//     --server against af's own serve.
//   - model and agent of a fresh session ride OPENCODE_CONFIG_CONTENT (model, default_agent);
//     neither is a secret, so the command string is fine.
//   - a session that already exists keeps its own model and agent whatever the config says
//     (measured), so a resume sets them through the API before the TUI attaches.
//   - a fork is made through the API too, and the TUI then resumes the copy.
//
// The API calls use `opencode api --standalone`, a one-shot private server (~1 s each). Any
// failure stops the launch, prints the reason in the pane and leaves a non-zero status; the
// line's final status is the TUI's, because the caller appends the exit recorder after it.
func buildProgramV2(spec LaunchSpec, model, mode, ocid string, fork bool) string {
	agent := ""
	switch mode {
	case "plan":
		agent = "plan"
	case "normal":
		agent = "build"
	}
	tui := []string{spec.shellBin(), "--standalone", envOr("AGENT_OPENCODE_FLAGS", "--auto")}
	if ocid == "" {
		if cfg := freshSessionConfig(model, agent); cfg != "" {
			tui = append([]string{"OPENCODE_CONFIG_CONTENT=" + session.ShellQuote(cfg)}, tui...)
		}
		return strings.Join(tui, " ")
	}

	ses := session.ShellQuote(ocid)
	target := `"$AF_OC_SES"`
	// Each switch is a step of the condition that gates the TUI, with its output captured and
	// shown only when it fails: a plan agent that silently stayed on build would let the
	// session edit when the member chose plan.
	api := func(sub, body string) string {
		return `AF_OC_OUT=$(` + spec.shellBin() + ` api --standalone POST /api/session/` + target + "/" + sub + " -d " + session.ShellQuote(body) + " 2>&1)"
	}
	var switches []string
	if prov, id, ok := splitModel(model); ok {
		ref := map[string]string{"providerID": prov, "id": id}
		if base, variant, found := strings.Cut(id, "#"); found && base != "" && variant != "" {
			ref["id"], ref["variant"] = base, variant
		}
		b, _ := json.Marshal(map[string]any{"model": ref})
		switches = append(switches, api("model", string(b)))
	}
	if agent != "" {
		b, _ := json.Marshal(map[string]string{"agent": agent})
		switches = append(switches, api("agent", string(b)))
	}
	if !fork && len(switches) == 0 {
		return strings.Join(append(tui, "--session", ses), " ")
	}
	fail := func(what string) string {
		return `echo "agent-fleet: opencode ` + what + `: $AF_OC_OUT" >&2; sleep 5; false`
	}
	cond := "true"
	if len(switches) > 0 {
		cond = strings.Join(switches, " && ")
	}
	run := strings.Join(append(tui, "--session", target), " ")
	if !fork {
		return "AF_OC_SES=" + ses + "; if " + cond + "; then " + run + "; else " + fail("could not apply the model or agent") + "; fi"
	}
	return `AF_OC_OUT=$(` + spec.shellBin() + ` api --standalone POST /api/session/` + ses + `/fork -d '{}' 2>&1); ` +
		`AF_OC_SES=$(printf '%s' "$AF_OC_OUT" | grep -o '"id":"ses_[A-Za-z0-9]*"' | head -n 1 | cut -d'"' -f4); ` +
		`if [ -z "$AF_OC_SES" ]; then ` + fail("fork failed") + `; elif ` + cond + `; then ` + run +
		`; else ` + fail("could not apply the model or agent") + `; fi`
}

// freshSessionConfig is the inline config that picks the model and primary agent of a
// session that does not exist yet; empty when neither is chosen.
func freshSessionConfig(model, agent string) string {
	cfg := map[string]string{}
	if _, _, ok := splitModel(model); ok {
		cfg["model"] = strings.TrimSpace(model)
	}
	if agent != "" {
		cfg["default_agent"] = agent
	}
	if len(cfg) == 0 {
		return ""
	}
	b, _ := json.Marshal(cfg)
	return string(b)
}
