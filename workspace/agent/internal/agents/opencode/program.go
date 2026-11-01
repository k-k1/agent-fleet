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

// buildProgram returns the --standalone tmux program for an opencode session (buildProgramV2).
// Env (AF_SESSION_SID + provider API keys) is carried on LaunchPlan.Env and injected by tmux
// `new-session -e`, never prefixed onto the command: that would put the keys into /proc/*/cmdline
// and pane_start_command in plaintext.
func buildProgram(model, mode string, ocid string, fork bool) string {
	return buildProgramV2(CurrentLaunch(), model, mode, ocid, fork)
}

// buildProgramV2 is the --standalone launch line for opencode 2: the fallback when af's own serve
// cannot be had and the AF_OPENCODE_TUI_STANDALONE=1 switch (shared.go is the default, one serve
// for every Terminal session). Measured on 2.0.28: the TUI takes only
// --standalone, --server, --auto, --continue, --session and --prompt, so --model, --agent and
// --fork (TUI flags of opencode 1, `opencode run` only on 2) are replaced by what it does support.
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
