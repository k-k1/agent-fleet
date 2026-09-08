package claude

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The init frame comes after the SessionStart hook frames and carries `skills` as bare names
// (shape measured on 2.1.263).
func TestParseInitSkills(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"hook_started","hook_name":"SessionStart:startup"}`,
		`{"type":"system","subtype":"hook_response","hook_name":"SessionStart:startup"}`,
		`not json at all`,
		`{"type":"system","subtype":"init","cwd":"/tmp/x","skills":["probe-user-skill","dataviz","simplify"],"slash_commands":["probe-user-skill","dataviz","simplify","compact","clear"]}`,
		`{"type":"result","subtype":"success","result":"/help isn't available in this environment."}`,
	}, "\n") + "\n"
	got, ok := parseInitSkills(strings.NewReader(stream))
	if !ok {
		t.Fatal("init frame not found")
	}
	if strings.Join(got, ",") != "probe-user-skill,dataviz,simplify" {
		t.Errorf("skills = %#v", got)
	}

	// no init frame (stdin closed before a message, say) → not found, so nothing is cached
	if _, ok := parseInitSkills(strings.NewReader(`{"type":"system","subtype":"hook_started"}` + "\n")); ok {
		t.Error("found an init frame in a stream without one")
	}
	// an init frame without skills (older CLI) → found with an empty list, which IS cached
	got, ok = parseInitSkills(strings.NewReader(`{"type":"system","subtype":"init"}` + "\n"))
	if !ok || got == nil || len(got) != 0 {
		t.Errorf("init without skills = %#v ok=%v", got, ok)
	}
}

// Live probe against the installed CLI. Opt-in (AF_LIVE_CLAUDE=1): it starts a real claude, so
// it needs the binary, a login, and about a second. Checks the contract the picker relies on —
// the frame arrives, is cached by binary identity, and the second call does not re-probe.
func TestBundledSkillsLive(t *testing.T) {
	if os.Getenv("AF_LIVE_CLAUDE") != "1" {
		t.Skip("set AF_LIVE_CLAUDE=1 to run the live probe")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed")
	}
	start := time.Now()
	names := BundledSkills()
	first := time.Since(start)
	if len(names) == 0 {
		t.Fatalf("no skills from the live probe (took %s)", first)
	}
	start = time.Now()
	again := BundledSkills()
	if second := time.Since(start); second > first/4 {
		t.Errorf("second call took %s (first %s) — not served from the cache", second, first)
	}
	if strings.Join(again, ",") != strings.Join(names, ",") {
		t.Errorf("cache returned a different list: %v vs %v", again, names)
	}
	t.Logf("%d skills in %s: %v", len(names), first, names)
}
