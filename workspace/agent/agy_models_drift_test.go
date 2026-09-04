//go:build drift

// Drift detection for agy's model catalog (Tier 1), the sibling of agy_pane_drift_test.go.
// The `drift` build tag keeps it out of a normal `go test ./...`: it needs the real agy
// binary and a real sign-in, because `agy models` calls an authenticated API.
//
// What it covers is the dependency on the OUTPUT FORMAT of `agy models`. In 1.1.19 that went
// from "display name only" to "id<TAB>display name", and product code that passed the whole
// line to `--model` was silently fallen back to the default by the CLI (docs/log/70
// §70.14.8).
//
// Why that went unnoticed is the reason this test exists: the session started, ran, and was
// quietly on a different model. The only sign was one warning line in the TUI; nothing
// reached the Console or the API. cli-release-watch already ran agy-contract.yml on a new
// release, but it only checked the TUI pane footer — nobody looked at the catalog.
package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
)

// TestDriftAgyModelsCatalog asserts that what agy.Models() hands the launch picker
// still matches the shape the real CLI prints — and, crucially, that the ids it
// produces are ids rather than whole lines.
func TestDriftAgyModelsCatalog(t *testing.T) {
	needBin(t, "agy")
	if !agy.SignedIn() {
		if os.Getenv("E2E_REQUIRE") == "1" {
			t.Fatal("agy is not signed in (E2E_REQUIRE=1 requires the real credential; `agy models` is an authenticated call)")
		}
		t.Skip("agy is not signed in — `agy models` needs a real token")
	}

	raw, err := exec.Command("agy", "models").Output()
	if err != nil {
		t.Fatalf("agy models failed: %v", err)
	}
	list := agy.Models()
	if len(list) == 0 {
		t.Fatal("the catalog is empty — the picker would offer the default only")
	}

	twoColumn := bytes.Contains(raw, []byte("\t"))
	t.Logf("agy models: %d entries, two-column=%v", len(list), twoColumn)

	for _, m := range list {
		if m.ID == "" || m.Label == "" {
			t.Fatalf("empty id or label: %+v", m)
		}
		// A banner or a note appearing on stdout would be picked up as a model. Today
		// "Fetching available models..." goes to stderr, so .Output() never sees it.
		if strings.HasSuffix(m.ID, "...") || strings.Contains(m.ID, "  ") {
			t.Fatalf("this does not look like a model, it looks like prose: %q", m.ID)
		}
		if !twoColumn {
			// Old format (the display name goes straight into --model). id == label
			// is the contract.
			if m.ID != m.Label {
				t.Fatalf("single-column output but id != label: %+v", m)
			}
			continue
		}
		// The one that matters: whitespace in an id under the two-column format
		// means the whole line is being passed, and the CLI silently falls back to
		// the default.
		if strings.ContainsAny(m.ID, " \t") {
			t.Fatalf("id contains whitespace — the whole line is being passed as --model: %q", m.ID)
		}
		if m.ID == m.Label {
			t.Fatalf("two-column output but id == label; the split did not happen: %+v", m)
		}
	}

	// If a third column appears, today's Cut stuffs everything left into the second one.
	// Not worth failing the run over, but not something to pass silently either.
	for _, ln := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if n := strings.Count(ln, "\t"); n > 1 {
			t.Errorf("a line has %d tabs — the catalog grew a column: %q", n+1, ln)
		}
	}
}
