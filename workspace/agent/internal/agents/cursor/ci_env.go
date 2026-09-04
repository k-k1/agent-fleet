package cursor

// Never pass the CI variable to the cursor CLI (docs/log/40 Track B).
//
// When the cursor CLI finds `CI` it shows no interactive UI: it paints the banner, never renders
// the composer, and ignores keystrokes (measured 2026-08-27, cursor 2026.08.25). Worse, the
// CLI's own startup log still prints `first_paint_ms` and reports a clean start, so from the
// outside everything looks healthy and only the UI is missing — which can only look like a hang.
// The TUI contract test on CI kept failing this way (tui_mirror_contract_test.go).
//
// The decision is made on PRESENCE, not on the value: `CI=` (the empty string) kills it just the
// same, and only an unset or `CI=false` brings it back (measured). Overwriting it with an empty
// string is therefore no remedy.
//
// The Workspace container itself does not set CI, but a user can add it through the Console's
// settings (environment variables). The moment they do, only cursor sessions become "a dead pane
// with nothing but a banner", and reaching the cause is as hard as described above. So AF strips
// it on every path that launches cursor. It is not extended to other kinds — copilot uses CI
// detection to suppress its self-update (docs/log/36), and stripping it everywhere would break
// that.

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// ciEnvVar is the variable name cursor uses to decide whether to show an interactive UI.
const ciEnvVar = "CI"

// EnvWithoutCI returns a new environment list with CI removed (the input is left alone). Only
// `CI` itself is dropped; near-names such as `CI_FOO` or `MY_CI` stay. Used on every path that
// builds an exec.Cmd (everything but the TUI: the ACP driver, the login PTY, the status/models
// probes, the assistant chat's headless run). It is exported because the chat's headless path is
// the one that calls the CLI from the main package.
func EnvWithoutCI(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if name, _, ok := strings.Cut(kv, "="); ok && name == ciEnvVar {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// unsetCIPrefix does the same for a tmux pane. What a pane gets is a shell string rather than an
// exec.Cmd, and tmux's `-e` can only set, never unset, so coreutils' `env -u` does the removal
// (emptying it with `CI=` has no effect — see above).
const unsetCIPrefix = "env -u " + ciEnvVar + " "

// probeCmd builds the exec.Cmd for a one-shot CLI probe (status / about / models / logout) in an
// environment with CI removed. These are non-interactive and measured to work even with CI set,
// but an exception per path means having to remember where it is stripped and where it is not.
// There is one rule: AF does not pass CI to cursor.
func probeCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin(), args...)
	cmd.Env = EnvWithoutCI(os.Environ())
	return cmd
}
