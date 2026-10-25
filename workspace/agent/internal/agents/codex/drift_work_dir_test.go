//go:build contract

// Can a managed codex thread's shell calls be given a per-session AF_WORK_DIR?
//
// The shared app-server's own environment is one value for every session, so the only
// per-session channel is the thread config. `shell_environment_policy.set` is the key that
// adds variables to the environment a thread's commands run with; thread/shellCommand runs a
// command the way a thread does, with no model involved, so it measures that without a turn.
package codex

import (
	"os"
	"path/filepath"
	"testing"
)

// Two concurrently loaded threads each see their own AF_WORK_DIR, and a variable the member's
// own config.toml sets in the same table survives (the thread map merges, it does not replace).
func TestDriftCodexThreadShellEnvironmentPolicyDeliversPerThreadEnv(t *testing.T) {
	cl := startDriftAppServerSeeded(t, func(home string) {
		codexHome := filepath.Join(home, ".codex")
		if err := os.MkdirAll(codexHome, 0o700); err != nil {
			t.Fatal(err)
		}
		body := "[shell_environment_policy.set]\nAF_DRIFT_FROM_FILE = \"kept\"\n"
		if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	out := t.TempDir()
	for _, slot := range []string{"slot_aaa", "slot_bbb"} {
		tid := startDriftThread(t, cl, map[string]any{
			"shell_environment_policy": map[string]any{
				"set": map[string]any{"AF_WORK_DIR": "/probe/" + slot},
			},
		})
		file := filepath.Join(out, slot)
		if _, err := cl.call("thread/shellCommand", map[string]any{
			"threadId": tid,
			"command":  `printf '%s|%s' "${AF_WORK_DIR-<unset>}" "${AF_DRIFT_FROM_FILE-<unset>}" > ` + file,
		}, 10e9); err != nil {
			t.Fatalf("thread/shellCommand: %v", err)
		}
		want := "/probe/" + slot + "|kept"
		if got := waitIdentityProbe(t, file); got != want {
			t.Fatalf("%s: shell saw %q, want %q — thread config shell_environment_policy.set does not "+
				"reach a thread's commands (or replaces the file layer), so a managed codex session "+
				"cannot get AF_WORK_DIR this way", slot, got, want)
		}
	}
}

// The control: without the thread key the command sees no AF_WORK_DIR, so the test above cannot
// pass by reading it from the daemon's environment.
func TestDriftCodexShellProbeReportsMissingWorkDir(t *testing.T) {
	cl := startDriftAppServer(t)
	tid := startDriftThread(t, cl, nil)
	file := filepath.Join(t.TempDir(), "none")
	if _, err := cl.call("thread/shellCommand", map[string]any{
		"threadId": tid,
		"command":  `printf '%s' "${AF_WORK_DIR-<unset>}" > ` + file,
	}, 10e9); err != nil {
		t.Fatalf("thread/shellCommand: %v", err)
	}
	if got := waitIdentityProbe(t, file); got != probeUnset {
		t.Fatalf("control read %q, want %q", got, probeUnset)
	}
}
