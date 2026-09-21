package muse

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Bin resolves the `muse` binary. Deployment installs it on demand under ~/.local/bin
// (ADR 0095 decision 8, kiro's shape), so PATH is the normal answer and the env override
// exists for a probe build deliberately kept outside PATH.
func Bin() string {
	if p := os.Getenv("AGENT_MUSE_BIN"); p != "" {
		return p
	}
	return "muse"
}

// Installed reports whether the binary is present. Muse Code is proprietary and is not in
// the distributed image (`BAKE_AGENT_CLIS=0`), so until a member runs the on-demand install
// this is false everywhere — which is what keeps the kind inert rather than half-wired.
func Installed() bool {
	p := Bin()
	if filepath.IsAbs(p) {
		st, err := os.Stat(p)
		return err == nil && !st.IsDir()
	}
	_, err := exec.LookPath(p)
	return err == nil
}

// ConfigHome is muse's own config root (~/.config/muse): settings.json, auth.json, the
// user-scope AGENTS.md and skills/. On the deny-list, because auth.json is the credential.
func ConfigHome() string { return filepath.Join(paths.HomeDir(), ".config", "muse") }

// DataHome is muse's session store root (~/.local/share/muse): every session's full
// transcript plus the user-wide session-name authority. On the deny-list for the same reason
// opencode's and kiro's stores are — a transcript is conversation content.
func DataHome() string { return filepath.Join(paths.HomeDir(), ".local", "share", "muse") }

// serveArgs builds the host's argv. Both posture flags are host-wide and fixed for its
// lifetime, which under decision 3's one-host-per-session shape makes them per-session
// decisions AF makes here.
//
//   - --disable-sandbox: bubblewrap cannot build a sandbox in a Workspace container
//     (AppArmor's docker-default refuses mount(2) and move_mount(2) whatever the
//     capabilities). The flag is asserted rather than assumed because WITHOUT it the failure
//     is silent: the toolCall comes back failed with "bwrap: Failed to make / slave" while
//     turn/completed still says terminal "completed", so the session looks healthy in the
//     Console and accomplishes nothing (ADR 0095 decision 5, measured).
//   - --trust-workspace: without it muse skips the repository's own AGENTS.md — measured, it
//     says so and carries on — and this project keeps its conventions there. Trust is not on
//     the wire (the string does not occur in the schema at all), so the flag is the only
//     route (decision 12).
func serveArgs() []string {
	return []string{"serve", "--disable-sandbox", "--trust-workspace"}
}

// childEnv is the spawned host's environment: the parent's, plus the clamps that have an
// environment route and the two paths muse reads its own state from.
//
// Three of Decision 6's eight clamps are environment variables and are applied here; the
// rest live in settings.json and are the settings writer's job. A clamp whose spelling is
// wrong is SILENTLY ineffective (measured — only a misspelling inside a strictly parsed
// settings section is loud, and that one kills the host with rc=3), so every name below was
// read out of the binary's own string table rather than transcribed from documentation.
func childEnv(base []string) []string {
	env := append([]string(nil), base...)
	env = append(env,
		// Never let the launcher replace the binary under a pin (decision 8).
		"MUSE_NO_AUTO_UPDATE=1",

		// Clamp 5, the one with a privacy cost: without it a real turn assembles the
		// member's own ~/.claude/CLAUDE.md into the model input and ships it to Meta.
		// Measured on `muse exec`: with this set the "Including your Claude Code and Codex
		// personal rules" banner disappears AND the marker planted in ~/.claude/CLAUDE.md
		// stops appearing in the durable log; with it unset or =0, both come back. The ADR
		// recorded only the settings.json route because `--no-foreign-personal-context` is
		// absent from `muse serve`; this variable is the cheaper belt, and the settings keys
		// stay the braces.
		"MUSE_EXPERIMENTAL_FOREIGN_PERSONAL_CONTEXT_KILL=1",

		// Clamp 6: the approval judge makes its own model call for every Prompt-bound
		// approval, and decision 5 keeps approvals on, so it would fire often. Its effect is
		// not measured — no settings key exists for it and `muse serve` has no flag — so it
		// is set on the evidence of the name alone and owes a behavioural test.
		"MUSE_DISABLE_APPROVAL_JUDGE=1",
	)
	// Clamp 2: four background observers each make their own model calls — invisible spend on
	// a metered account, invisible quota on a subscription — and they cost turn latency,
	// because the end-of-turn gate waits for them (measured: 17.9 s versus 4.5 s for the same
	// one-line answer). There is no settings key; these six are the route.
	for _, name := range observerVars {
		env = append(env, name+"=0")
	}
	return env
}

// observerVars are the six reminder observers. Read out of the binary's string table on
// 1.3.0-R3401.1; a name that stops existing in 1.4 silently stops clamping, which is what the
// drift check is for.
var observerVars = []string{
	"MUSE_EXPERIMENTAL_SKILL_REMINDER",
	"MUSE_EXPERIMENTAL_GOAL_REMINDER",
	"MUSE_EXPERIMENTAL_VERIFY_REMINDER",
	"MUSE_EXPERIMENTAL_TODO_REMINDER",
	"MUSE_EXPERIMENTAL_MEMORY_REMINDER",
	"MUSE_EXPERIMENTAL_SCOPE_REMINDER",
}
