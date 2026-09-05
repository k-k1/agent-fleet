//go:build drift

// Tier 1 drift detection for the wording of a plan verdict (the ExitPlanMode tool_result).
// The `drift` build tag keeps it out of a normal `go test ./...` — it needs a real CLI or
// real transcripts.
//
// Why it exists: the approved/rejected badge is produced by reading wording claude returns
// that is not part of any contract. Once the approval result started carrying the whole
// approved plan body (everything below `## Approved Plan:`), keyword matching picked up a
// "rejected" inside the plan body and put a rejected badge on an approved plan. Unit tests
// only pin the shapes we already know, so they cannot detect such a change at all.
//
// Two tests:
//   - TestDriftClaudePlanResultLiterals — is the wording we read still present in the real
//     CLI binary? Needs no credentials and no real turn, so it can run daily
//     (cli-drift.yml). Note that a string being present does not prove it is still used in
//     that position, so a false green is possible; proving the whole path is the job of
//     claude_plan_contract_test.go (a real TUI).
//   - TestDriftClaudePlanResultsInRealTranscripts — classify the verdicts left in this
//     machine's actual transcripts with the production reader and look for results that
//     read as neither approved nor rejected. Approval options CI cannot reach ("Yes, and
//     manually approve edits" and the like) show up here as far as the real fleet used
//     them. Meant to be run inside a Workspace.
package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// planResultLiterals are the strings the production code depends on. If one disappears,
// either the badge decision or the truncation of the embedded plan body breaks silently.
var planResultLiterals = []string{
	// The approval header (planDecision.ts isApproved / plan_verdict.go planApprovedRe).
	"User has approved your plan",
	// Start of the plan body embedded in an approval result (planAnswerHead /
	// PLAN_BODY_MARKER). Lose track of it and the plan body flows into the badge decision,
	// reproducing the wrong-badge failure.
	"## Approved Plan:",
	// The rejection side. The Console's reject button is an interrupt (Escape), so the
	// verdict is left in this shape.
	"Request interrupted by user for tool use",
}

func TestDriftClaudePlanResultLiterals(t *testing.T) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		needBin(t, "claude") // fails when E2E_REQUIRE=1, skips otherwise
		return
	}
	if p, err := filepath.EvalSymlinks(bin); err == nil {
		bin = p
	}
	if v, err := exec.Command("claude", "--version").Output(); err == nil {
		t.Logf("claude version: %s (%s)", strings.TrimSpace(string(v)), bin)
	}
	// Today it is a single binary (2.1.251 is a 214MB claude.exe), but it used to be a JS
	// bundle. Look at both the binary itself and the scripts under the package so either
	// shape is found.
	files := []string{bin}
	if dir := filepath.Dir(filepath.Dir(bin)); dir != "" {
		for _, pat := range []string{"*.js", "*.cjs", "*.mjs"} {
			if m, _ := filepath.Glob(filepath.Join(dir, pat)); m != nil {
				files = append(files, m...)
			}
		}
	}
	for _, lit := range planResultLiterals {
		found := false
		for _, f := range files {
			if ok, err := fileContains(f, lit); err == nil && ok {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q is gone from the CLI — the plan verdict wording may have changed.\n"+
				"Capture one real sample and realign console/src/features/mirror/planDecision.ts\n"+
				"and workspace/agent/internal/agents/claude/plan_verdict.go\n"+
				"(left alone, the approved/rejected badge silently inverts).", lit)
		}
	}
}

// fileContains streams f looking for needle, so a 200MB+ binary is not read into memory.
func fileContains(path, needle string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	const chunk = 4 << 20
	pat := []byte(needle)
	buf := make([]byte, chunk+len(pat))
	keep := 0 // retain the tail of the previous chunk so a match across the boundary is not lost
	for {
		n, err := f.Read(buf[keep:])
		if n > 0 && bytes.Contains(buf[:keep+n], pat) {
			return true, nil
		}
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if keep+n >= len(pat) {
			keep = copy(buf, buf[keep+n-len(pat)+1:keep+n])
		} else {
			keep += n
		}
	}
}

func TestDriftClaudePlanResultsInRealTranscripts(t *testing.T) {
	root := os.Getenv("CLAUDE_CONFIG_DIR")
	if root == "" {
		root = filepath.Join(os.Getenv("HOME"), ".claude")
	}
	logs, _ := filepath.Glob(filepath.Join(root, "projects", "*", "*.jsonl"))
	if len(logs) == 0 {
		t.Skipf("no transcripts (%s) — this test is meant to run inside a real Workspace", root)
	}
	total, unknown := 0, 0
	for _, p := range logs {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var lines [][]byte
		for _, ln := range bytes.Split(b, []byte("\n")) {
			if len(bytes.TrimSpace(ln)) > 0 {
				lines = append(lines, ln)
			}
		}
		// Run the production reader as-is (Answer has already been through planAnswerHead).
		for _, turn := range claude.CollectTurns(lines, 0, len(lines)) {
			for _, part := range turn.Parts {
				if part.Kind != "plan" || part.Answer == "" {
					continue
				}
				total++
				if claude.PlanVerdict(part.Answer) == claude.PlanUnknown {
					unknown++
					t.Errorf("%s: verdict reads as neither approved nor rejected = wording drift:\n  %q",
						filepath.Base(p), transcript.CapOutput(part.Answer))
				}
				// A plan body still present in an approval result means the truncation did not work.
				if part.Plan != "" && strings.Contains(part.Answer, strings.TrimSpace(part.Plan)) {
					t.Errorf("%s: the verdict still carries the whole plan body = planAnswerHead's marker changed",
						filepath.Base(p))
				}
			}
		}
	}
	t.Logf("plan verdicts in real transcripts: %d (%d unclassifiable) across %d files", total, unknown, len(logs))
	if total == 0 {
		t.Skip("not a single settled plan — drift cannot be judged on this host")
	}
}
