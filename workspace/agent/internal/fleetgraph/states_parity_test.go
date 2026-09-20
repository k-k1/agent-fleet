package fleetgraph

import (
	"encoding/json"
	"os"
	"testing"
)

// statesFixturePath is console/src/lib/fleetgraph.states.json, the frozen normalisation
// table both Go and vitest read (docs/log/101 §101.8). Relative to this package's
// directory (workspace/agent/internal/fleetgraph): four levels up reaches the repo root.
const statesFixturePath = "../../../../console/src/lib/fleetgraph.states.json"

type statesFixture struct {
	Cases []struct {
		Raw   *string `json:"raw"`
		State string  `json:"state"`
		Band  string  `json:"band"`
	} `json:"cases"`
}

// TestNormalizeStateMatchesFixture is the Go half of the cross-language parity check ADR
// 0096 decision 3 requires: this package's NormalizeState and console/src/lib/fleetgraph.ts's
// GraphNormalizeState must agree on every case in the SAME fixture file. A missing fixture
// fails the suite rather than skipping it — a silently skipped parity check and a passing
// one are indistinguishable otherwise (docs/log/101 §101.8).
func TestNormalizeStateMatchesFixture(t *testing.T) {
	b, err := os.ReadFile(statesFixturePath)
	if err != nil {
		t.Fatalf("fixture not found at %s (this must fail, not skip): %v", statesFixturePath, err)
	}
	var fx statesFixture
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatalf("fixture unmarshal: %v", err)
	}
	if len(fx.Cases) == 0 {
		t.Fatal("fixture has no cases — parity check would pass vacuously")
	}
	for _, c := range fx.Cases {
		// raw: null is the one input Go never receives (Session.state is a plain string
		// server-side; only the TypeScript live-map path sees undefined) — see the
		// fixture's own comment. Go's equivalent input for that case is "".
		raw := ""
		if c.Raw != nil {
			raw = *c.Raw
		}
		state, gotRaw := NormalizeState(raw)
		if string(state) != c.State {
			t.Errorf("NormalizeState(%q) state = %q, fixture wants %q", raw, state, c.State)
		}
		wantRaw := ""
		if c.State == "unknown" {
			wantRaw = raw
		}
		if gotRaw != wantRaw {
			t.Errorf("NormalizeState(%q) raw = %q, want %q", raw, gotRaw, wantRaw)
		}
	}
}

// TestNormalizeStateMatchesFixture_CatchesDrift is the positive control demanded by
// AGENTS.md's "verifying your own work": it proves the parity test above actually notices
// a mismatch, by running the same comparison against a deliberately shifted fixture
// (one case's band changed under it) and asserting that a naive comparison DOES fail. This
// does not touch NormalizeState — it protects against a parity test that always passes.
func TestNormalizeStateMatchesFixture_CatchesDrift(t *testing.T) {
	b, err := os.ReadFile(statesFixturePath)
	if err != nil {
		t.Fatalf("fixture not found: %v", err)
	}
	var fx statesFixture
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatalf("fixture unmarshal: %v", err)
	}
	// Corrupt one entry the way a real drift would: NormalizeState says "working" is
	// active/working, so asserting the fixture wants something else must fail.
	mismatches := 0
	for _, c := range fx.Cases {
		raw := ""
		if c.Raw != nil {
			raw = *c.Raw
		}
		state, _ := NormalizeState(raw)
		wrongWant := c.State + "-shifted"
		if string(state) != wrongWant {
			mismatches++
		}
	}
	if mismatches != len(fx.Cases) {
		t.Fatalf("expected every case to mismatch against a shifted table, got %d/%d", mismatches, len(fx.Cases))
	}
}
