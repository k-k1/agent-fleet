package claude

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// buildProgram must emit the official fork command on a fork's FIRST launch
// (no own jsonl yet): claude resumes the SOURCE sid, --fork-session branches it,
// and --session-id pins the new id to OUR deterministic sid (verified: --session-id
// sets the fork's id). Without forkFrom it's a plain new session.
func TestBuildProgramFork(t *testing.T) {
	os.Unsetenv("AGENT_SESSION_CMD")
	// A random sid has no jsonl on disk, so we hit the first-launch branch.
	got := buildProgram("00000000-0000-4000-8000-0000000000fk", "", "", "", "", "11111111-1111-4111-8111-111111111src", true)
	// sid / forkFrom are shell-quoted like every other flag value.
	want := "--resume '11111111-1111-4111-8111-111111111src' --fork-session --session-id '00000000-0000-4000-8000-0000000000fk'"
	if !strings.Contains(got, want) {
		t.Fatalf("fork program = %q, want it to contain %q", got, want)
	}

	// No forkFrom + no jsonl → plain new session, never --fork-session.
	plain := buildProgram("22222222-2222-4222-8222-222222222new", "", "", "", "", "", true)
	if !strings.Contains(plain, "--session-id '22222222-2222-4222-8222-222222222new'") || strings.Contains(plain, "--fork-session") {
		t.Fatalf("new-session program = %q, want --session-id and no --fork-session", plain)
	}
}

func TestBuildProgramEffortAndPlanMode(t *testing.T) {
	os.Unsetenv("AGENT_SESSION_CMD")
	got := buildProgram("33333333-3333-4333-8333-333333333new", "sonnet", "high", "plan", "", "", false)
	for _, want := range []string{"--model 'sonnet'", "--effort 'high'", "--permission-mode plan", "--allow-dangerously-skip-permissions"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in %q", want, got)
		}
	}
	if strings.Contains(got, " --dangerously-skip-permissions") {
		t.Fatalf("plan launch must not force bypass mode: %q", got)
	}
}

// The plan-mode rewrite is token-wise: an AGENT_CLAUDE_FLAGS that already carries
// --allow-dangerously-skip-permissions must not become --allow-allow-….
func TestBuildProgramPlanModeKeepsAllowFlagIntact(t *testing.T) {
	os.Unsetenv("AGENT_SESSION_CMD")
	t.Setenv("AGENT_CLAUDE_FLAGS", "--allow-dangerously-skip-permissions --verbose")
	got := buildProgram("44444444-4444-4444-8444-444444444new", "", "", "plan", "", "", false)
	if strings.Contains(got, "--allow-allow-") {
		t.Fatalf("token replacement mangled an existing --allow flag: %q", got)
	}
	if !strings.Contains(got, "--allow-dangerously-skip-permissions --verbose") {
		t.Fatalf("existing flags lost: %q", got)
	}
}

// Permission prompts on (docs/log/76: an ordinary launch by a user who turned skipping
// off). --dangerously-skip-permissions is swapped for --allow-…, which allows nothing by
// itself and only leaves the route into bypass via shift+tab inside the TUI. This is not
// plan, so --permission-mode plan must not be added.
func TestBuildProgramPermissionsOn(t *testing.T) {
	os.Unsetenv("AGENT_SESSION_CMD")
	got := buildProgram("55555555-5555-4555-8555-555555555new", "", "", "", "", "", false)
	if !strings.Contains(got, "--allow-dangerously-skip-permissions") {
		t.Errorf("permissions-on program %q lacks --allow-dangerously-skip-permissions", got)
	}
	if strings.Contains(got, " --dangerously-skip-permissions") {
		t.Errorf("permissions-on program must not force bypass: %q", got)
	}
	if strings.Contains(got, "--permission-mode plan") {
		t.Errorf("normal launch must not start in plan: %q", got)
	}
}

// TestBuildProgramBlocksNativePeerChannel: claude's own cross-session channel must be
// blocked at launch (docs/log/58 §58.17, ADR 0041 decision 1).
//
// If this goes red, a claude↔claude route AF cannot see is open in every session. The env
// in the Dockerfile used to be the effective block, but 2.1.251 was measured punching
// through it, so this is now the only block there is. The two values work in opposite
// directions (deny on the sending side, refuse on the receiving side), so this also checks
// that only one of them has not survived.
func TestBuildProgramBlocksNativePeerChannel(t *testing.T) {
	os.Unsetenv("AGENT_SESSION_CMD")
	got := buildProgram("66666666-6666-4666-8666-666666666new", "", "", "", "", "", true)
	if !strings.Contains(got, "--settings '"+nativePeerSettings+"'") {
		t.Fatalf("not blocked via --settings: %q", got)
	}
	for _, want := range []string{`"deny":["ListAgents","SendMessage"]`, `"crossSessionInbound":"refuse"`} {
		if !strings.Contains(nativePeerSettings, want) {
			t.Errorf("the settings lack %s: %s", want, nativePeerSettings)
		}
	}
	// The value is embedded in a shell command. The JSON contains only double quotes, so
	// wrapping it in single quotes is safe — and this notices if it ever grows one.
	if strings.Contains(nativePeerSettings, "'") {
		t.Errorf("settings containing a single quote break under ShellQuote: %s", nativePeerSettings)
	}
	// A user's own flags cannot override it, since it is appended after them.
	t.Setenv("AGENT_CLAUDE_FLAGS", "--verbose")
	if !strings.Contains(buildProgram("77777777-7777-4777-8777-777777777new", "", "", "", "", "", true), "--settings '") {
		t.Error("setting AGENT_CLAUDE_FLAGS makes the block disappear")
	}
}

// launchSettingsOf decodes the one --settings argument of a built command line.
func launchSettingsOf(t *testing.T, program string) map[string]any {
	t.Helper()
	if n := strings.Count(program, "--settings"); n != 1 {
		t.Fatalf("want exactly one --settings, got %d: %q", n, program)
	}
	_, rest, _ := strings.Cut(program, "--settings '")
	raw, _, ok := strings.Cut(rest, "'")
	if !ok {
		t.Fatalf("--settings argument is not single-quoted: %q", program)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("--settings is not valid JSON (%v): %s", err, raw)
	}
	return m
}

// TestBuildProgramSwitchesAutoMemoryOffWithAFMemory: while the Agent memory switch is on, every
// launch shape (new, resume, drifted resume, fork) carries autoMemoryEnabled:false in its one
// --settings, and with the switch off or unwired none does (ADR 0108 decision 6 step 2). The
// JSON is decoded, so a malformed constant fails here rather than at claude's start.
func TestBuildProgramSwitchesAutoMemoryOffWithAFMemory(t *testing.T) {
	cfg := isolateSlot(t)
	t.Cleanup(func() { AutoMemoryOff = nil })
	const sid = "88888888-8888-4888-8888-888888888new"
	writeSlotJSONL(t, cfg, "-tmp-repo", testSlotSID)
	writeSlotJSONL(t, cfg, "-tmp-repo", testLiveSID)

	shapes := map[string]func() string{
		"new":    func() string { return buildProgram(sid, "", "", "", "", "", true) },
		"plan":   func() string { return buildProgram(sid, "m", "e", "plan", "lbl", "", false) },
		"fork":   func() string { return buildProgram(sid, "", "", "", "", "99999999-9999-4999-8999-999999999999", true) },
		"resume": func() string { return buildProgram(testSlotSID, "", "", "", "", "", true) },
		"flags-env": func() string {
			t.Setenv("AGENT_CLAUDE_FLAGS", "--verbose")
			return buildProgram(sid, "", "", "", "", "", true)
		},
		"drifted-resume": func() string {
			sids.Write(testSlotSID, testLiveSID)
			return buildProgram(testSlotSID, "", "", "", "", "", true)
		},
	}
	// The resume shapes must really take the --resume branch, or they would test nothing.
	for _, name := range []string{"resume", "drifted-resume"} {
		AutoMemoryOff = nil
		if got := shapes[name](); !strings.Contains(got, "claude --resume ") {
			t.Fatalf("%s: not a resume command: %q", name, got)
		}
	}

	for _, tc := range []struct {
		label string
		hook  func() bool
		off   bool
	}{
		{"unwired", nil, false},
		{"switch off", func() bool { return false }, false},
		{"switch on", func() bool { return true }, true},
	} {
		AutoMemoryOff = tc.hook
		for name, build := range shapes {
			m := launchSettingsOf(t, build())
			v, has := m["autoMemoryEnabled"]
			if tc.off && v != false {
				t.Errorf("%s/%s: autoMemoryEnabled = %v, want false", tc.label, name, v)
			}
			if !tc.off && has {
				t.Errorf("%s/%s: autoMemoryEnabled must be absent, got %v", tc.label, name, v)
			}
			// The peer-channel block survives in both variants.
			if m["crossSessionInbound"] != "refuse" {
				t.Errorf("%s/%s: crossSessionInbound lost: %v", tc.label, name, m)
			}
			perms, _ := m["permissions"].(map[string]any)
			if deny, _ := perms["deny"].([]any); len(deny) != 2 {
				t.Errorf("%s/%s: permissions.deny lost: %v", tc.label, name, m)
			}
		}
	}
	if strings.Contains(nativePeerSettingsNoAutoMemory, "'") {
		t.Errorf("settings containing a single quote break under ShellQuote")
	}
}
