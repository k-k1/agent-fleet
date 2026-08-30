package claude

import (
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

// 権限確認あり（docs/log/76: 利用者がスキップをオフにした通常起動）。--dangerously-skip-
// permissions は --allow-… へ差し替わる（それ自体は何も許可せず、TUI 内 shift+tab で
// bypass へ入る道だけを残す）。plan ではないので --permission-mode plan は付かない。
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
