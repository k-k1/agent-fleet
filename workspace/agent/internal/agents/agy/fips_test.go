package agy

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Env must not write through to the caller's slice. The spawn sites pass
// `append(os.Environ(), "TERM=…")`, which usually has spare capacity, and appending into it
// would hand a second caller an env it never asked for.
func TestEnvDoesNotMutateCaller(t *testing.T) {
	base := append(make([]string, 0, 8), "A=1", "B=2")
	got := Env(base)
	if len(base) != 2 || base[0] != "A=1" || base[1] != "B=2" {
		t.Fatalf("caller slice changed: %q", base)
	}
	if len(got) < len(base) {
		t.Fatalf("Env dropped entries: %q", got)
	}
	for i := range base {
		if got[i] != base[i] {
			t.Fatalf("Env reordered the base env: %q", got)
		}
	}
	if extra := len(got) - len(base); extra != len(MaskEnv()) {
		t.Errorf("Env appended %d entries, MaskEnv has %d", extra, len(MaskEnv()))
	}
}

// The mask reaches the tmux pane through LaunchPlan.Env (tmux -e), never as a prefix on the
// program string: a command-string prefix would land in /proc/*/cmdline and in tmux's
// pane_start_command, and it would be lost the moment AGENT_AGY_CMD overrides the program.
func TestBuildLaunchCarriesMaskInEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	plan, err := New().BuildLaunch(session.Meta{Name: "s", Dir: dir, Kind: session.KindAgy}, agents.LaunchOpts{})
	if err != nil {
		// Unsupported host (no agy binary, or a mask that does not help): nothing to assert.
		t.Skipf("BuildLaunch refused on this host: %v", err)
	}
	want := MaskEnv()
	if len(plan.Env) != len(want) {
		t.Fatalf("plan.Env = %q, want %q", plan.Env, want)
	}
	for i := range want {
		if plan.Env[i] != want[i] {
			t.Fatalf("plan.Env = %q, want %q", plan.Env, want)
		}
	}
	if strings.Contains(plan.Program, "OPENSSL_ia32cap") {
		t.Errorf("the mask leaked into the program string: %q", plan.Program)
	}
}
