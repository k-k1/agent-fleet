package session

import "testing"

// The range and the fallback. Out of range reads as the DEFAULT rather than as the nearest
// bound: the Console offers a fixed set of choices, so a value outside them is a hand-edited or
// stale prefs file, and answering it with a number nobody picked would be worse than answering
// it with what the workspace does unconfigured.
func TestNormalizeSpawnChildLimit(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, SpawnChildLimitDefault}, // missing: uiprefs returns 0 for absent and malformed alike
		{-1, SpawnChildLimitDefault},
		{1, 1},
		{3, 3},
		{SpawnChildLimitMax, SpawnChildLimitMax},
		{SpawnChildLimitMax + 1, SpawnChildLimitDefault},
		{1000, SpawnChildLimitDefault},
	} {
		if got := NormalizeSpawnChildLimit(tc.in); got != tc.want {
			t.Errorf("NormalizeSpawnChildLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// Unwired means the default, so a binary that never links uiprefs (and every caller written
// before the setting existed) keeps the historical behaviour instead of reading zero as "no
// children at all".
func TestSpawnChildLimitDefaultsWhenNoPrefIsWired(t *testing.T) {
	old := SpawnChildLimitPref
	t.Cleanup(func() { SpawnChildLimitPref = old })

	SpawnChildLimitPref = nil
	if got := SpawnChildLimit(); got != SpawnChildLimitDefault {
		t.Fatalf("unwired limit = %d, want %d", got, SpawnChildLimitDefault)
	}
	SpawnChildLimitPref = func() int { return 5 }
	if got := SpawnChildLimit(); got != 5 {
		t.Fatalf("wired limit = %d, want 5", got)
	}
	// The accessor normalizes, so no caller has to: a stored 99 must not become the ceiling
	// quoted in a refusal.
	SpawnChildLimitPref = func() int { return 99 }
	if got := SpawnChildLimit(); got != SpawnChildLimitDefault {
		t.Fatalf("out-of-range limit = %d, want %d", got, SpawnChildLimitDefault)
	}
}

// Read afresh every call. Snapshotting it into a package variable is the mistake this guards:
// the user can change the setting between two turns of one session, and the number a refusal
// names has to be the number in force (ADR 0073 decision 6 — no invisible limits).
func TestSpawnChildLimitIsReadOnEveryCall(t *testing.T) {
	old := SpawnChildLimitPref
	t.Cleanup(func() { SpawnChildLimitPref = old })

	n := 2
	SpawnChildLimitPref = func() int { return n }
	if got := SpawnChildLimit(); got != 2 {
		t.Fatalf("limit = %d, want 2", got)
	}
	n = 6
	if got := SpawnChildLimit(); got != 6 {
		t.Fatalf("limit after the setting changed = %d, want 6 (snapshotted?)", got)
	}
}
