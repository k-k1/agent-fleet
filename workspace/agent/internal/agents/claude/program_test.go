package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
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
	if !strings.Contains(got, "--permission-mode default") {
		t.Errorf("permissions-on launch must start in manual mode: %q", got)
	}
	// bypass and plan are unchanged.
	if b := buildProgram("55555555-5555-4555-8555-555555555bym", "", "", "", "", "", true); strings.Contains(b, "--permission-mode") {
		t.Errorf("bypass launch must not set a permission mode: %q", b)
	}
	if p := buildProgram("55555555-5555-4555-8555-555555555pln", "", "", "plan", "", "", false); strings.Contains(p, "--permission-mode default") {
		t.Errorf("plan launch must not also set default: %q", p)
	}
	// An explicit mode in AGENT_CLAUDE_FLAGS is not duplicated.
	t.Setenv("AGENT_CLAUDE_FLAGS", "--permission-mode acceptEdits")
	if o := buildProgram("55555555-5555-4555-8555-555555555ovr", "", "", "", "", "", false); strings.Contains(o, "--permission-mode default") {
		t.Errorf("AGENT_CLAUDE_FLAGS mode overridden: %q", o)
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
func TestLaunchPinsUsageLimitContinuation(t *testing.T) {
	t.Setenv("AGENT_SESSION_CMD", "")
	for _, mode := range []string{"af", "native", "off"} {
		settings := launchSettingsOf(t, buildProgramPinned([]string{"TEST_ROUTE=kept"}, "sid", "", "", "", "", "", true, false, mode))
		if got := settings["autoContinueAtUsageLimit"]; got != (mode == "native") {
			t.Errorf("mode %s: autoContinueAtUsageLimit = %v", mode, got)
		}
		if env, _ := settings["env"].(map[string]any); env["TEST_ROUTE"] != "kept" {
			t.Errorf("mode %s: route settings lost: %v", mode, settings)
		}
	}
}

func TestBuildProgramSwitchesAutoMemoryOffWithAFMemory(t *testing.T) {
	cfg := isolateSlot(t)
	t.Cleanup(func() { AutoMemoryOff = nil })
	const sid = "88888888-8888-4888-8888-888888888new"
	// Two independent resume slots: the plain one has its own jsonl and no ledger entry; the
	// drifted one's ledger entry points at a different, live id.
	const plainSlot = "b7000000-0000-5000-8000-0000000plain"
	writeSlotJSONL(t, cfg, "-tmp-repo", plainSlot)
	writeSlotJSONL(t, cfg, "-tmp-repo", testLiveSID)

	shapes := map[string]func() string{
		"new":    func() string { return buildProgram(sid, "", "", "", "", "", true) },
		"plan":   func() string { return buildProgram(sid, "m", "e", "plan", "lbl", "", false) },
		"fork":   func() string { return buildProgram(sid, "", "", "", "", "99999999-9999-4999-8999-999999999999", true) },
		"resume": func() string { return buildProgram(plainSlot, "", "", "", "", "", true) },
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
	for name, id := range map[string]string{"resume": plainSlot, "drifted-resume": testLiveSID} {
		AutoMemoryOff = nil
		if got := shapes[name](); !strings.HasPrefix(got, "claude --resume '"+id+"' ") {
			t.Fatalf("%s: want a resume of %s: %q", name, id, got)
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
			if deny, _ := perms["deny"].([]any); len(deny) != 2 || deny[0] != "ListAgents" || deny[1] != "SendMessage" {
				t.Errorf("%s/%s: permissions.deny lost: %v", tc.label, name, m)
			}
		}
	}
	if strings.Contains(nativePeerSettingsNoAutoMemory, "'") {
		t.Errorf("settings containing a single quote break under ShellQuote")
	}
}

// Prompts on: every launch that starts a conversation carries --permission-mode default, and an
// ordinary resume of an existing transcript does not (the member's shift+tab choice survives a
// restart). The first launch of a point fork resumes a transcript that was just written, but is
// still a fresh start.
func TestBuildProgramPermissionsOnFreshVersusResume(t *testing.T) {
	cfg := isolateSlot(t)
	t.Setenv("AGENT_SESSION_CMD", "")
	const (
		newSID    = "aa000000-0000-4000-8000-0000000000new"
		resumeSID = "aa000000-0000-4000-8000-0000000resume"
		pointSID  = "aa000000-0000-4000-8000-00000000point"
	)
	writeSlotJSONL(t, cfg, "-tmp-repo", resumeSID)
	writeSlotJSONL(t, cfg, "-tmp-repo", pointSID)
	const want = "--permission-mode default"
	cases := []struct {
		name string
		got  string
		has  bool
	}{
		{"new", buildProgram(newSID, "", "", "", "", "", false), true},
		{"whole-conversation fork, first launch", buildProgram(newSID, "", "", "", "", "99999999-9999-4999-8999-999999999999", false), true},
		{"ordinary resume", buildProgram(resumeSID, "", "", "", "", "", false), false},
		{"restart of a fork (ForkFrom set, transcript exists)", buildProgram(resumeSID, "", "", "", "", "99999999-9999-4999-8999-999999999999", false), false},
		{"point fork, first launch", buildProgramPinned(nil, pointSID, "", "", "", "", "99999999-9999-4999-8999-999999999999", false, true), true},
		{"point fork, later restart", buildProgramPinned(nil, pointSID, "", "", "", "", "99999999-9999-4999-8999-999999999999", false, false), false},
	}
	for _, c := range cases {
		if strings.Contains(c.got, want) != c.has {
			t.Errorf("%s: contains %q = %v, want %v: %q", c.name, want, !c.has, c.has, c.got)
		}
	}
	for _, name := range []string{"ordinary resume", "point fork, first launch"} {
		for _, c := range cases {
			if c.name == name && !strings.HasPrefix(c.got, "claude --resume ") {
				t.Errorf("%s: expected the resume branch: %q", name, c.got)
			}
		}
	}
}

// Only a real flag token counts as an explicit mode: a --name that merely contains the text must
// not suppress the default, and the `=` form is recognised.
func TestBuildProgramPermissionModeDetection(t *testing.T) {
	isolateSlot(t)
	t.Setenv("AGENT_SESSION_CMD", "")
	got := buildProgram("bb000000-0000-4000-8000-0000000000new", "", "", "", "調査 --permission-mode auto", "", false)
	if !strings.Contains(got, " --permission-mode default") {
		t.Errorf("a label containing the flag text suppressed the default: %q", got)
	}
	for _, f := range []string{"--permission-mode=acceptEdits", "--verbose\t--permission-mode\tacceptEdits", "'--permission-mode' acceptEdits"} {
		t.Setenv("AGENT_CLAUDE_FLAGS", f)
		if got := buildProgram("bb000000-0000-4000-8000-0000000000new", "", "", "", "", "", false); strings.Contains(got, "--permission-mode default") {
			t.Errorf("AGENT_CLAUDE_FLAGS %q was overridden: %q", f, got)
		}
	}
}

// A quoted value of another flag that holds the text is not an explicit mode.
func TestBuildProgramPermissionModeQuotedValueIsNotAFlag(t *testing.T) {
	isolateSlot(t)
	t.Setenv("AGENT_SESSION_CMD", "")
	t.Setenv("AGENT_CLAUDE_FLAGS", `--name "調査 --permission-mode auto"`)
	got := buildProgram("bb000000-0000-4000-8000-0000000000new", "", "", "", "", "", false)
	if !strings.Contains(got, " --permission-mode default") {
		t.Errorf("a quoted --name value suppressed the default: %q", got)
	}
}

// The word after a single-value option is its value, even when it spells the flag.
func TestHasPermissionModeFlagSkipsOptionValues(t *testing.T) {
	for flags, want := range map[string]bool{
		`--name --permission-mode`:                    false,
		`--name --permission-mode --permission-mode`:  true,
		`--model opus --permission-mode plan`:         true,
		`--name x --permission-mode=plan`:             true,
		`--verbose --permission-mode plan`:            true,
		`--append-system-prompt --permission-mode -n`: false,
	} {
		if got := hasPermissionModeFlag(flags); got != want {
			t.Errorf("hasPermissionModeFlag(%q) = %v, want %v", flags, got, want)
		}
	}
}

// A launch retried after the transcript was written (a later step failed) is still the first
// launch of the point fork and starts in manual mode.
func TestBuildLaunchPointForkRetryAfterFailureStaysManual(t *testing.T) {
	cfg := isolateSlot(t)
	dir := t.TempDir()
	const srcSid = "11111111-1111-4111-8111-111111111111"
	var buf strings.Builder
	for _, ln := range convo(t) {
		buf.Write(ln)
		buf.WriteByte('\n')
	}
	pdir := filepath.Join(cfg, "projects", "-tmp-x")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, srcSid+".jsonl"), []byte(buf.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	off := false
	m := session.Meta{Kind: session.KindClaude, Name: "pf-retry", Dir: dir, ForkFrom: srcSid, ForkAt: "u2", SkipPermissions: &off}
	// A route whose settings conflict fails after the transcript is written.
	bad := m
	bad.Route = sel(RouteAPI, "")
	_, badErr := agentImpl{}.BuildLaunch(bad, agents.LaunchOpts{})
	if badErr == nil {
		t.Fatal("the route launch was expected to fail after the transcript was written")
	}
	retry, err := agentImpl{}.BuildLaunch(m, agents.LaunchOpts{})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !strings.Contains(retry.Program, "--permission-mode default") {
		t.Errorf("retried first launch lost manual mode: %q", retry.Program)
	}
	next, err := agentImpl{}.BuildLaunch(m, agents.LaunchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(next.Program, "--permission-mode default") {
		t.Errorf("a later launch must be a plain resume: %q", next.Program)
	}
}

// The production wiring: BuildLaunch computes pointFork before MaterializeForkAt writes the
// branch transcript, so the first launch of a prompts-on point fork starts in manual mode and
// the next launch (a plain resume of that transcript) does not.
func TestBuildLaunchPointForkStartsManualOnlyOnce(t *testing.T) {
	cfg := isolateSlot(t)
	dir := t.TempDir()
	const srcSid = "11111111-1111-4111-8111-111111111111"
	var buf strings.Builder
	for _, ln := range convo(t) {
		buf.Write(ln)
		buf.WriteByte('\n')
	}
	pdir := filepath.Join(cfg, "projects", "-tmp-x")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, srcSid+".jsonl"), []byte(buf.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	off := false
	m := session.Meta{Kind: session.KindClaude, Name: "pf-mode", Dir: dir, ForkFrom: srcSid, ForkAt: "u2", SkipPermissions: &off}
	first, err := agentImpl{}.BuildLaunch(m, agents.LaunchOpts{})
	if err != nil {
		t.Fatalf("first launch: %v", err)
	}
	if !strings.Contains(first.Program, "--permission-mode default") {
		t.Errorf("first point-fork launch must start in manual mode: %q", first.Program)
	}
	second, err := agentImpl{}.BuildLaunch(m, agents.LaunchOpts{})
	if err != nil {
		t.Fatalf("second launch: %v", err)
	}
	if !strings.Contains(second.Program, "claude --resume ") || strings.Contains(second.Program, "--permission-mode default") {
		t.Errorf("a later launch must be a plain resume keeping the recorded mode: %q", second.Program)
	}
}
