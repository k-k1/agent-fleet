package claude

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// imageClaudeEnv is the claude-related part of the image's ENV block (workspace/Dockerfile).
var imageClaudeEnv = []string{
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	"DISABLE_TELEMETRY=1",
	"DISABLE_ERROR_REPORTING=1",
	"DISABLE_AUTOUPDATER=1",
}

// setRemoteControl writes the toggle the Console writes (PUT /claude/settings), or leaves the
// settings file absent for on == nil.
func setRemoteControl(t *testing.T, on *bool) {
	t.Helper()
	if on == nil {
		return
	}
	if err := writeSettings(map[string]any{"remoteControlAtStartup": *on}); err != nil {
		t.Fatal(err)
	}
}

// claudeEnvOf runs program the way tmux does (`sh -c`) from the image environment, with a
// stand-in `claude` that records the environment it was started with, and returns that
// environment as a set of KEY=VALUE lines.
func claudeEnvOf(t *testing.T, program string) map[string]bool {
	t.Helper()
	bin, out := t.TempDir(), filepath.Join(t.TempDir(), "env.txt")
	script := "#!/bin/sh\nenv > '" + out + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", program)
	cmd.Env = append([]string{"PATH=" + bin + ":/usr/bin:/bin"}, imageClaudeEnv...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("running %q: %v\n%s", program, err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the stand-in claude did not run for %q: %v", program, err)
	}
	got := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		got[l] = true
	}
	return got
}

func hasVar(env map[string]bool, name string) bool {
	for l := range env {
		if strings.HasPrefix(l, name+"=") {
			return true
		}
	}
	return false
}

// Remote Control refuses to start while CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC is set (it
// turns feature-flag evaluation off), so the toggle being on must remove it from the claude
// process, on every launch route. The other three image variables must stay in every case,
// and with the toggle off or never written the environment is exactly the image's.
func TestLaunchEnvFollowsRemoteControlToggle(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name     string
		toggle   *bool
		wantNess bool
	}{
		{"toggle on", &yes, false},
		{"toggle off", &no, true},
		{"never written", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := isolateSlot(t)
			setRemoteControl(t, tc.toggle)
			// Three routes through buildProgram: a new session, a resume (a jsonl exists for
			// the slot), and the first launch of a fork.
			resumed := "88888888-8888-4888-8888-888888888res"
			writeSlotJSONL(t, cfg, "p", resumed)
			for route, program := range map[string]string{
				"new":    buildProgram("99999999-9999-4999-8999-999999999new", "", "", "", "", "", true),
				"resume": buildProgram(resumed, "", "", "", "", "", true),
				"fork":   buildProgram("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaafrk", "", "", "", "", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbsrc", true),
			} {
				env := claudeEnvOf(t, program)
				if got := hasVar(env, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"); got != tc.wantNess {
					t.Errorf("%s: NONESSENTIAL_TRAFFIC present = %v, want %v (program %q)", route, got, tc.wantNess, program)
				}
				for _, keep := range imageClaudeEnv[1:] {
					if !env[keep] {
						t.Errorf("%s: %s missing from the claude process", route, keep)
					}
				}
			}
		})
	}
}

// The same through the launch plan startSessionTmux consumes, so a future caller of
// BuildLaunch cannot bypass the toggle by building its own program.
func TestBuildLaunchEnvFollowsRemoteControlToggle(t *testing.T) {
	for _, on := range []bool{true, false} {
		isolateSlot(t)
		setRemoteControl(t, &on)
		plan, err := agentImpl{}.BuildLaunch(session.Meta{Kind: session.KindClaude, Name: "rc-env", Dir: t.TempDir()}, agents.LaunchOpts{})
		if err != nil {
			t.Fatal(err)
		}
		env := claudeEnvOf(t, plan.Program)
		if got := hasVar(env, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"); got == on {
			t.Errorf("toggle %v: NONESSENTIAL_TRAFFIC present = %v", on, got)
		}
	}
}
