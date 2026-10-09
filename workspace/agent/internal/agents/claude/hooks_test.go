package claude

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureStatusHooksShape pins the session-state hook wiring: the simple events, the
// SessionStart→boot reset, and the catch-all PostToolUse→working heartbeat that keeps a
// long turn from false-idling when the working status file is lost mid-turn.
func TestEnsureStatusHooksShape(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	EnsureStatusHooks()
	hooks := readHooks(t, dir)

	// Simple matcher-less events carry the right state command.
	for event, want := range map[string]string{
		"UserPromptSubmit": "session-status working",
		"Stop":             "session-status idle",
		"SessionStart":     "session-status boot",
		"Notification":     "session-status permission",
	} {
		if b, _ := json.Marshal(hooks[event]); !strings.Contains(string(b), want) {
			t.Errorf("hooks[%q] = %s, want it to contain %q", event, b, want)
		}
	}

	// PostToolUse is a single catch-all (empty matcher) → working heartbeat.
	post, _ := json.Marshal(hooks["PostToolUse"])
	if !strings.Contains(string(post), `"matcher":""`) || !strings.Contains(string(post), "session-status working") {
		t.Errorf("PostToolUse = %s, want a catch-all (matcher \"\") → working", post)
	}
	if strings.Contains(string(post), "AskUserQuestion") || strings.Contains(string(post), "ExitPlanMode") {
		t.Errorf("PostToolUse should have migrated off the per-tool matchers: %s", post)
	}
}

// TestEnsureStatusHooksIdempotent confirms a second call is a no-op (stable settings) —
// EnsureStatusHooks runs on every agent startup and must not churn settings.json.
func TestEnsureStatusHooksIdempotent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	EnsureStatusHooks()
	first, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	EnsureStatusHooks()
	second, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("settings.json changed on the second EnsureStatusHooks call:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// TestEnsureStatusHooksMigratesOldPostToolUse checks that settings carrying the legacy
// two-matcher PostToolUse (AskUserQuestion/ExitPlanMode) get rewritten to the catch-all.
func TestEnsureStatusHooksMigratesOldPostToolUse(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	seed := map[string]any{"hooks": map[string]any{
		"PostToolUse": []any{
			map[string]any{"matcher": "AskUserQuestion", "hooks": []any{map[string]any{"type": "command", "command": statusHookCmd("working")}}},
			map[string]any{"matcher": "ExitPlanMode", "hooks": []any{map[string]any{"type": "command", "command": statusHookCmd("working")}}},
		},
	}}
	b, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}

	EnsureStatusHooks()
	post, _ := json.Marshal(readHooks(t, dir)["PostToolUse"])
	if strings.Contains(string(post), "AskUserQuestion") || !strings.Contains(string(post), `"matcher":""`) {
		t.Errorf("old PostToolUse not migrated to catch-all: %s", post)
	}
}

func readHooks(t *testing.T, dir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	h, ok := m["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("no hooks in settings: %s", b)
	}
	return h
}

// TestEnsureStatusHooksPreservesUserHooks: a user's own hooks on the same events
// (incl. a matcher-less PostToolUse entry) must survive installation, and the AF
// heartbeat must still be installed alongside them.
func TestEnsureStatusHooksPreservesUserHooks(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	seed := map[string]any{"hooks": map[string]any{
		"Stop": []any{
			map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "/home/u/notify.sh"}}},
		},
		"PostToolUse": []any{
			map[string]any{"matcher": "", "hooks": []any{map[string]any{"type": "command", "command": "/home/u/log-tools.sh"}}},
		},
	}}
	b, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}

	EnsureStatusHooks()
	h := readHooks(t, dir)
	stop, _ := json.Marshal(h["Stop"])
	if !strings.Contains(string(stop), "notify.sh") || !strings.Contains(string(stop), "session-status") {
		t.Errorf("Stop must keep the user hook AND add ours: %s", stop)
	}
	post, _ := json.Marshal(h["PostToolUse"])
	if !strings.Contains(string(post), "log-tools.sh") || !strings.Contains(string(post), "session-status") {
		t.Errorf("PostToolUse must keep the user matcher-less entry AND install the heartbeat: %s", post)
	}
}

// A hook installed by a build that has since been deleted (a dev build under /tmp, an
// e2e or smoke copy) must be repointed at the usable agent — otherwise every claude
// event silently fails and the session looks frozen in the Console. The presence checks
// match on the command CONTENT, so without an explicit repair a stale path survives
// forever.
func TestEnsureStatusHooksRepairsDeadExePath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	installed := filepath.Join(t.TempDir(), "workspace-agent")
	if err := os.WriteFile(installed, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_AGENT_INSTALLED_BIN", installed)
	want := statusHookCmd("idle")

	// Ours, but pinned to a binary that is gone — plus a user hook that must not move.
	seed := map[string]any{"hooks": map[string]any{
		"Stop": []any{
			map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "/tmp/af-agent session-status idle"}}},
			map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "/tmp/notify.sh"}}},
		},
	}}
	b, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}

	EnsureStatusHooks()
	stop, _ := json.Marshal(readHooks(t, dir)["Stop"])
	if strings.Contains(string(stop), "/tmp/af-agent") {
		t.Errorf("dead exe path still wired: %s", stop)
	}
	if !strings.Contains(string(stop), want) {
		t.Errorf("Stop = %s, want the repaired command %q", stop, want)
	}
	if !strings.Contains(string(stop), "/tmp/notify.sh") {
		t.Errorf("a user hook in a volatile dir is their business, not ours to rewrite: %s", stop)
	}
	// Repair is idempotent: a second pass leaves settings.json byte-identical.
	first, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	EnsureStatusHooks()
	second, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if string(first) != string(second) {
		t.Errorf("repair churns settings.json:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestRepointStatusHookCmd(t *testing.T) {
	const want = "/usr/local/bin/workspace-agent"
	// A different but WORKING path is left alone (a host install is legitimate).
	// It has to live outside the volatile roots to count as working, so: /bin/sh.
	const live = "/bin/sh"
	for _, c := range []struct {
		cmd, out string
		ok       bool
	}{
		{"/tmp/af-agent session-status idle", want + " session-status idle", true},
		{"/tmp/af-agent session-status working sid123 codex", want + " session-status working sid123 codex", true},
		{"/tmp/af-agent session-push-notification", want + " session-push-notification", true},
		{want + " session-status idle", "", false},           // already right
		{live + " session-status idle", "", false},           // other path, still runnable
		{"/tmp/notify.sh --loud", "", false},                 // not ours
		{"/tmp/af-agent statusline --af-capture", "", false}, // ours, but not a status hook
		{"", "", false},
	} {
		out, ok := repointStatusHookCmd(c.cmd, want)
		if ok != c.ok || out != c.out {
			t.Errorf("repointStatusHookCmd(%q) = (%q,%v) want (%q,%v)", c.cmd, out, ok, c.out, c.ok)
		}
	}
}

// pushEntries returns the PostToolUse entries on the PushNotification matcher, split
// into ours (session-push-notification) and everyone else's.
func pushEntries(t *testing.T, dir string) (ours, theirs int) {
	t.Helper()
	arr, _ := readHooks(t, dir)["PostToolUse"].([]any)
	for _, e := range arr {
		em, _ := e.(map[string]any)
		if em["matcher"] != pushToolMatcher {
			continue
		}
		if b, _ := json.Marshal(em["hooks"]); strings.Contains(string(b), PushHookSubcommand) {
			ours++
		} else {
			theirs++
		}
	}
	return ours, theirs
}

// PushNotification's local notification is only an OSC sequence, which is dropped for
// claude panes; the PostToolUse forwarder is its one route to the notification center.
func TestEnsureStatusHooksInstallsPushForwarderOnce(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	EnsureStatusHooks()
	EnsureStatusHooks()
	if ours, _ := pushEntries(t, dir); ours != 1 {
		t.Fatalf("PushNotification forwarder installed %d times, want 1", ours)
	}
	// The heartbeat is still the matcher-less entry next to it.
	if !postToolUseHasAF(readHooks(t, dir)) {
		t.Fatal("catch-all heartbeat missing next to the PushNotification forwarder")
	}
}

// A user's own hook on the same matcher stays, and does not stand in for ours.
func TestEnsureStatusHooksPushForwarderKeepsUserEntry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	seed := map[string]any{"hooks": map[string]any{
		"PostToolUse": []any{
			map[string]any{"matcher": "PushNotification", "hooks": []any{map[string]any{"type": "command", "command": "/home/u/phone.sh"}}},
		},
	}}
	b, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}

	EnsureStatusHooks()
	EnsureStatusHooks()
	if ours, theirs := pushEntries(t, dir); ours != 1 || theirs != 1 {
		t.Fatalf("PushNotification entries: ours=%d theirs=%d, want 1 and 1", ours, theirs)
	}
}

// Installing a missing heartbeat strips our session-status PostToolUse entries; the
// forwarder must be there exactly once afterwards, neither dropped nor doubled.
func TestEnsureStatusHooksPushForwarderSurvivesHeartbeatInstall(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	seed := map[string]any{"hooks": map[string]any{
		"PostToolUse": []any{
			map[string]any{"matcher": "PushNotification", "hooks": []any{map[string]any{"type": "command", "command": pushHookCmd()}}},
		},
	}}
	b, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}

	EnsureStatusHooks()
	if ours, _ := pushEntries(t, dir); ours != 1 {
		t.Fatalf("PushNotification forwarder present %d times after the heartbeat install, want 1", ours)
	}
}

// The forwarder must not be a session-status state: settings.json can point at an
// older agent, whose session-status would persist any unknown word as the session's
// status. A subcommand of its own is rejected by an older dispatcher instead.
func TestPushHookIsNotASessionStatusState(t *testing.T) {
	f := strings.Fields(pushHookCmd())
	if len(f) != 2 || f[1] != PushHookSubcommand || f[1] == "session-status" {
		t.Fatalf("pushHookCmd() = %q, want `<exe> %s` and nothing else", pushHookCmd(), PushHookSubcommand)
	}
}

// modelSwitchEntries counts PreModelSwitch entries running our allow command, and the rest.
func modelSwitchEntries(t *testing.T, dir string) (ours, others int) {
	t.Helper()
	arr, _ := readHooks(t, dir)["PreModelSwitch"].([]any)
	for _, e := range arr {
		if b, _ := json.Marshal(e); strings.Contains(string(b), "PreModelSwitch") && strings.Contains(string(b), `\"permissionDecision\":\"allow\"`) {
			ours++
		} else {
			others++
		}
	}
	return ours, others
}

// The allow hook is what keeps /model from stopping on claude's "Switch model?" dialog;
// it is installed once however often the agent starts, and prints valid allow JSON.
func TestEnsureStatusHooksInstallsModelSwitchAllowOnce(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	EnsureStatusHooks()
	EnsureStatusHooks()
	if ours, _ := modelSwitchEntries(t, dir); ours != 1 {
		t.Fatalf("PreModelSwitch allow installed %d times, want 1", ours)
	}

	out, err := exec.Command("sh", "-c", modelSwitchAllowCmd).Output()
	if err != nil {
		t.Fatalf("run hook command: %v", err)
	}
	var got struct {
		Out struct {
			Event    string `json:"hookEventName"`
			Decision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("hook output %q is not JSON: %v", out, err)
	}
	if got.Out.Event != "PreModelSwitch" || got.Out.Decision != "allow" {
		t.Errorf("hook output = %s, want PreModelSwitch allow", out)
	}
}

// A user's own PreModelSwitch hook stays and does not stand in for ours.
func TestEnsureStatusHooksModelSwitchKeepsUserEntry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	seed := `{"hooks":{"PreModelSwitch":[{"hooks":[{"type":"command","command":"/home/dev/my-gate.sh"}]}]}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	EnsureStatusHooks()
	EnsureStatusHooks()
	ours, others := modelSwitchEntries(t, dir)
	if ours != 1 || others != 1 {
		t.Fatalf("PreModelSwitch ours=%d others=%d, want 1 and 1", ours, others)
	}
}
