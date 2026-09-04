//go:build clicontract

// copilot contract test (it SPENDS REAL TURNS): drift detection for forking at a message
// (docs/log/55).
//
// copilot has no official fork entry point either, so the session-state directory is copied
// and events.jsonl truncated. The surgery works only because events.jsonl is what a restore
// reads from, and that can only be established by measurement: session.db sits next to it,
// and if that one won, the mirror would show the history cut while the agent still
// remembered all of it. A synthetic test stays green either way, so this is the only alarm.
//
//	COPILOT_CONTRACT_LIVE=1 go test -tags clicontract -run TestContractLiveCopilotForkAt ./internal/agents/copilot/
//
// Cost: 3 real turns (one-line replies). COPILOT_HOME is isolated, so the real ~/.copilot is
// never touched (authentication uses the environment's GitHub token / saved credential).
package copilot

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func requireCopilotLive(t *testing.T) {
	t.Helper()
	if os.Getenv("COPILOT_CONTRACT_LIVE") != "1" {
		t.Skip("COPILOT_CONTRACT_LIVE!=1 — real-credential copilot contract skipped")
	}
	if _, err := exec.LookPath("copilot"); err != nil {
		t.Skipf("copilot not on PATH: %v", err)
	}
}

func copilotUUID(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("/proc/sys/kernel/random/uuid")
	if err != nil {
		t.Skipf("no uuid source: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// copilotPrompt runs one headless turn in home/dir and returns its output.
func copilotPrompt(t *testing.T, home, dir, sid, prompt string) string {
	t.Helper()
	cmd := exec.Command("copilot", "--session-id", sid, "-p", prompt)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "COPILOT_HOME="+home)
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
		t.Fatalf("copilot -p timed out (%q)", prompt)
	}
	if err != nil {
		t.Fatalf("copilot -p %q: %v\n%s", prompt, err, out)
	}
	return string(out)
}

func TestContractLiveCopilotForkAt(t *testing.T) {
	requireCopilotLive(t)

	home := t.TempDir() // isolation: the real ~/.copilot is never touched
	work := t.TempDir()
	t.Setenv("COPILOT_HOME", home)
	// HOME is NOT replaced — copilot's authentication comes from there (replacing it exits
	// 1). The single sids entry written below is cleaned up instead.

	src := copilotUUID(t)
	copilotPrompt(t, home, work, src, "Remember the codeword ALPHA. Reply exactly: OK")
	copilotPrompt(t, home, work, src, "Forget that. The codeword is now BETA. Reply exactly: OK")

	// Anchors come from the production transcript path, so a change in the event id shape
	// fails right here.
	m := session.Meta{Dir: work, Name: "cp-contract", Kind: session.KindCopilot}
	slot := session.UUID(work, "cp-contract")
	sids.Write(slot, src)
	t.Cleanup(func() { sids.Remove(slot) })
	td, _ := (agentImpl{}).Transcript(m)
	var anchors []string
	for _, tn := range td.Turns {
		if tn.Role == "user" && tn.AnchorID != "" {
			anchors = append(anchors, tn.AnchorID)
		}
	}
	if len(anchors) < 2 {
		t.Fatalf("found %d anchored user turns in a real transcript, want >= 2 — events.jsonl's "+
			"id field or the user.message shape moved, so \"fork from here\" has nothing to point at", len(anchors))
	}

	resolved, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: anchors[1]})
	if err != nil {
		t.Fatalf("ResolveForkAt: %v", err)
	}
	dst := copilotUUID(t)
	if err := MaterializeForkAt(src, dst, resolved); err != nil {
		t.Fatalf("MaterializeForkAt against a REAL session failed: %v", err)
	}
	// Everything the source had was carried over, i.e. the copy is complete. Never assert
	// on individual file names: by 1.0.81 copilot had dropped the per-session `session.db`
	// and moved the state to a global `session-store.db` directly under COPILOT_HOME
	// (measured 2026-08-28; sessions created before that still have session.db). Names
	// here would misreport "the copy broke" on every such relocation. The claim of this
	// test is that even when the carried state contains both turns, the truncated
	// events.jsonl wins.
	srcEntries, err := os.ReadDir(filepath.Join(home, "session-state", src))
	if err != nil {
		t.Fatalf("cannot read the source session-state: %v", err)
	}
	for _, e := range srcEntries {
		if _, err := os.Stat(filepath.Join(home, "session-state", dst, e.Name())); err != nil {
			t.Fatalf("branch is missing %q from the source session-state (the copy is incomplete): %v", e.Name(), err)
		}
	}

	out := copilotPrompt(t, home, work, dst, "What is the codeword? Answer with one word.")
	up := strings.ToUpper(out)
	switch {
	case strings.Contains(up, "ALPHA"):
		// As contracted: events.jsonl is what the restore reads from.
	case strings.Contains(up, "BETA"):
		t.Fatalf("the branch remembered the turn we cut away — copilot no longer restores from "+
			"events.jsonl (session.db, which we copy verbatim, now wins). Every point fork would "+
			"silently carry history the mirror shows as removed.\n%s", out)
	default:
		t.Fatalf("the branch could not answer from the carried history — a truncated events.jsonl "+
			"is no longer resumable the way docs/log/55 §55.5 measured.\n%s", out)
	}
}
