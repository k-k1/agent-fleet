//go:build clicontract

// A claude contract test that CONSUMES REAL TURNS: the only drift detection for branching
// at a past message (docs/log/55).
//
// Why it is needed: claude alone cannot use the official fork-point API, because
// `--resume-session-at` exists only in print mode and AF launches claude as a TUI. Instead
// AF truncates the transcript jsonl itself, which breaks silently the moment claude's
// transcript schema or its interpretation of resume moves. The breakage takes the form of
// "it launches but the conversation is wrong", which a synthetic test can never notice.
// ADR 0039 decision 9 requires running this one test at every CLI pin update.
//
// Only two contracts are pinned here:
//  1. claude can resume a jsonl truncated by hand (the launch is not refused)
//  2. it works from the truncated history alone (it does not remember the cut-away turns)
//
// Opt-in, because it uses a real credential and subscription quota:
//
//	CLAUDE_CONTRACT_LIVE=1 go test -tags clicontract -run TestContractLiveClaudeForkAt ./internal/agents/claude/
//
// Cost: three haiku turns, each a one-line reply. The working conversation is created in a
// scratch project directory and removed, transcript and all, during cleanup.
package claude

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireClaudeLive(t *testing.T) {
	t.Helper()
	if os.Getenv("CLAUDE_CONTRACT_LIVE") != "1" {
		t.Skip("CLAUDE_CONTRACT_LIVE!=1 — real-credential claude contract skipped")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skipf("claude not on PATH: %v", err)
	}
}

// claudeUUID mints a session id claude accepts for --session-id.
func claudeUUID(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("/proc/sys/kernel/random/uuid")
	if err != nil {
		t.Skipf("no uuid source: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// claudePrint runs one headless turn and returns its output.
func claudePrint(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("claude", append(args, "--model", "haiku")...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(240 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("claude %v timed out", args)
	}
	if err != nil {
		t.Fatalf("claude %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestContractLiveClaudeForkAt(t *testing.T) {
	requireClaudeLive(t)

	// A scratch working dir gives the conversation its own project folder under the real
	// config dir (auth lives there, so it can't be isolated) — removed at the end.
	work := t.TempDir()
	src := claudeUUID(t)
	dst := claudeUUID(t)

	claudePrint(t, work, "--session-id", src, "-p", "Remember the codeword ALPHA. Reply exactly: OK")
	claudePrint(t, work, "--resume", src, "-p", "Forget that. The codeword is now BETA. Reply exactly: OK")

	paths := jsonlPaths(src)
	if len(paths) == 0 {
		t.Fatal("no transcript written for the source session")
	}
	projectDir := filepath.Dir(paths[0])
	t.Cleanup(func() {
		// Remove this scratch conversation's transcript so the real config dir is not left dirty.
		_ = os.RemoveAll(projectDir)
	})

	lines, _, _ := TranscriptRead(src)
	turns := CollectTurns(lines, 0, len(lines))
	var anchors []string
	for _, tn := range turns {
		if tn.Role == "user" && tn.AnchorID != "" {
			anchors = append(anchors, tn.AnchorID)
		}
	}
	if len(anchors) < 2 {
		t.Fatalf("found %d anchored user turns in a real transcript, want >= 2 — claude's uuid "+
			"field or the user-line shape moved, so \"branch from here\" has nothing to point at", len(anchors))
	}

	// Branch before the SECOND prompt: the branch must remember ALPHA and not BETA.
	if err := MaterializeForkAt(src, dst, anchors[1]); err != nil {
		t.Fatalf("MaterializeForkAt against a REAL transcript failed: %v — the cut-point rules in "+
			"forkat.go no longer match what claude writes", err)
	}
	out := claudePrint(t, work, "--resume", dst, "-p", "What is the codeword? Answer with one word.")
	up := strings.ToUpper(out)
	switch {
	case strings.Contains(up, "ALPHA"):
		// As contracted: it works from the truncated history alone.
	case strings.Contains(up, "BETA"):
		t.Fatalf("the branch remembered the turn we cut away (answered %q) — claude no longer "+
			"reconstructs the conversation from the file we wrote, so every point fork silently "+
			"carries history it should not", out)
	default:
		t.Fatalf("the branch could not answer from the carried history (%q) — a truncated transcript "+
			"is no longer resumable the way docs/log/55 §55.2 measured", out)
	}
}
