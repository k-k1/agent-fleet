package codex

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// missingBinaryOut is what the npm launcher prints when codex's platform package is absent
// (codex 0.167.0, 2026-11-06).
const missingBinaryOut = "file:///x/codex.js:108\n  throw new Error(\n\nError: Missing optional dependency @openai/codex-linux-x64. Reinstall Codex: npm install -g @openai/codex@latest\n    at findCodexExecutable (file:///x/codex.js:108:9)\n\nNode.js v22.23.4\n"

// A codex that exits is reported with its own output, not as an account setting.
func TestDeviceStartNoURLExitedShowsOutput(t *testing.T) {
	got := deviceStartNoURL(true, missingBinaryOut)
	if !strings.Contains(got, "Missing optional dependency @openai/codex-linux-x64") {
		t.Fatalf("message lacks codex's error: %q", got)
	}
	if strings.Contains(got, "may be disabled") {
		t.Fatalf("an exited codex is blamed on the account setting: %q", got)
	}
	if got := deviceStartNoURL(true, ""); got != "codex exited without printing a device-auth URL" {
		t.Fatalf("no output: %q", got)
	}
}

func TestDeviceStartNoURLRunningKeepsAccountHint(t *testing.T) {
	if got := deviceStartNoURL(false, ""); !strings.Contains(got, "may be disabled for this account") {
		t.Fatalf("running codex lost the account hint: %q", got)
	}
}

func TestOutputTailKeepsOrder(t *testing.T) {
	if got := outputTail("a\n\nb\n c \nd\n", 3); got != "b / c / d" {
		t.Fatalf("outputTail = %q", got)
	}
}

// WaitFor gives up as soon as the child has exited instead of sitting out its timeout, and
// still matches output that arrived just before the exit.
func TestWaitForStopsWhenFlowEnds(t *testing.T) {
	f, err := agents.StartFlow(exec.Command("sh", "-c", "echo boom; exit 1"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	start := time.Now()
	if m := f.WaitFor(regexp.MustCompile(`https://\S+`), 10*time.Second); m != "" {
		t.Fatalf("matched %q", m)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("WaitFor waited %v after the child exited", d)
	}
	if !f.Ended() || !strings.Contains(f.Clean(), "boom") {
		t.Fatalf("ended=%v output=%q", f.Ended(), f.Clean())
	}

	g, err := agents.StartFlow(exec.Command("sh", "-c", "echo https://auth.example/x; exit 0"))
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if m := g.WaitFor(regexp.MustCompile(`https://\S+`), 10*time.Second); m != "https://auth.example/x" {
		t.Fatalf("output printed right before exit was not matched: %q", m)
	}
}
