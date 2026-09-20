package fleetgraph

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func withTempState(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func mustMillis(t *testing.T, s string) int64 {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tm.UnixMilli()
}

func lineageOf(t *testing.T, page FleetGraphPage, ev string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range page.Lineage {
		b, _ := json.Marshal(raw)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		if m["ev"] == ev {
			out = append(out, m)
		}
	}
	return out
}

// TestBuildPage_DeathAndReviveSurviveOutsideWindow is docs/log/101 §101.8's landmine: a
// lane born day1, stopped day2, resumed day3, queried with a day4 24h window must still
// show the stop — clipping lineage to birth-only makes the whole stopped day vanish.
func TestBuildPage_DeathAndReviveSurviveOutsideWindow(t *testing.T) {
	withTempState(t)
	RecordBirth(Birth{Name: "sage1", Kind: "claude", Origin: OriginUser})
	setLastLineageTs(t, "2026-09-01T00:00:00.000Z")
	RecordDeath("sage1", "", 0, 0)
	setLastLineageTs(t, "2026-09-02T00:00:00.000Z")
	RecordRevive("sage1")
	setLastLineageTs(t, "2026-09-03T00:00:00.000Z")

	since := mustMillis(t, "2026-09-04T00:00:00.000Z")
	until := mustMillis(t, "2026-09-05T00:00:00.000Z")
	page, err := BuildPage(since, until)
	if err != nil {
		t.Fatal(err)
	}
	if len(lineageOf(t, page, "birth")) != 1 {
		t.Fatalf("want 1 birth event carried in despite being outside the window, got %v", lineageOf(t, page, "birth"))
	}
	if len(lineageOf(t, page, "death")) != 1 {
		t.Fatalf("want 1 death event carried in (the bug this guards: clipping to birth-only drops it), got %v", page.Lineage)
	}
	if len(lineageOf(t, page, "revive")) != 1 {
		t.Fatalf("want 1 revive event carried in, got %v", page.Lineage)
	}
}

// TestBuildPage_LaneNotOverlappingWindowIsExcluded is the converse: a lane that died and
// was never revived, entirely before the window, must not appear at all.
func TestBuildPage_LaneNotOverlappingWindowIsExcluded(t *testing.T) {
	withTempState(t)
	RecordBirth(Birth{Name: "gone1", Kind: "claude", Origin: OriginUser})
	setLastLineageTs(t, "2026-01-01T00:00:00.000Z")
	RecordDeath("gone1", "", 0, 0)
	setLastLineageTs(t, "2026-01-01T01:00:00.000Z")

	since := mustMillis(t, "2026-09-04T00:00:00.000Z")
	until := mustMillis(t, "2026-09-05T00:00:00.000Z")
	page, err := BuildPage(since, until)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Lineage) != 0 {
		t.Fatalf("want no lineage for a lane entirely outside the window, got %v", page.Lineage)
	}
}

// TestBuildPage_AncestorBirthIncludedForFamilyOrdering (ADR 0096 decision 9): a child
// whose own lineage overlaps the window must carry its parent's birth along too, even
// though the parent itself never touches the window.
func TestBuildPage_AncestorBirthIncludedForFamilyOrdering(t *testing.T) {
	withTempState(t)
	RecordBirth(Birth{Name: "parent1", Kind: "claude", Origin: OriginUser})
	setLastLineageTs(t, "2020-01-01T00:00:00.000Z")
	RecordBirth(Birth{Name: "child1", Kind: "claude", Origin: OriginSession, OriginSession: "parent1"})
	setLastLineageTs(t, "2026-09-04T12:00:00.000Z")

	since := mustMillis(t, "2026-09-04T00:00:00.000Z")
	until := mustMillis(t, "2026-09-05T00:00:00.000Z")
	page, err := BuildPage(since, until)
	if err != nil {
		t.Fatal(err)
	}
	births := lineageOf(t, page, "birth")
	names := map[string]bool{}
	for _, b := range births {
		names[b["name"].(string)] = true
	}
	if !names["parent1"] {
		t.Fatalf("want parent1's birth included as ancestor context, got %v", births)
	}
	if !names["child1"] {
		t.Fatalf("want child1's own birth included, got %v", births)
	}
}

// TestBuildPage_ActivityClippedToWindow: unlike lineage, activity IS clipped.
func TestBuildPage_ActivityClippedToWindow(t *testing.T) {
	withTempState(t)
	setClock(t, "2026-09-04T12:00:00.000Z")
	RecordInstruct("conv:abc", "s1", "operator", "do the thing")
	setClock(t, "2026-01-01T00:00:00.000Z")
	RecordInstruct("conv:abc", "s1", "operator", "old, outside window")

	since := mustMillis(t, "2026-09-04T00:00:00.000Z")
	until := mustMillis(t, "2026-09-05T00:00:00.000Z")
	page, err := BuildPage(since, until)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Activity) != 1 {
		t.Fatalf("want exactly 1 in-window activity event, got %d: %v", len(page.Activity), page.Activity)
	}
}

// TestObserveState_WritesOnlyOnChange guards against the double-observer duplicate
// (Console 4s + CP reaper 1m hitting the same handler, docs/log/101 §101.2).
func TestObserveState_WritesOnlyOnChange(t *testing.T) {
	withTempState(t)
	resetLastStateForTest()
	ObserveState("s1", "working")
	ObserveState("s1", "working") // same state, from a second observer — must not duplicate
	ObserveState("s1", "idle")

	since := int64(0)
	until := clockNow().Add(time.Hour).UnixMilli()
	page, err := BuildPage(since, until)
	if err != nil {
		t.Fatal(err)
	}
	var stateEvents []map[string]any
	for _, raw := range page.Activity {
		b, _ := json.Marshal(raw)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		if m["ev"] == "state" {
			stateEvents = append(stateEvents, m)
		}
	}
	if len(stateEvents) != 2 {
		t.Fatalf("want 2 state transitions (working, then idle), got %d: %v", len(stateEvents), stateEvents)
	}
	if stateEvents[1]["from"] != "working" {
		t.Fatalf("second transition's from = %v, want working", stateEvents[1]["from"])
	}
}

// TestNormalizeState_UnknownNeverFoldsToIdle is ADR 0096 decision 3's central invariant.
func TestNormalizeState_UnknownNeverFoldsToIdle(t *testing.T) {
	for _, raw := range []string{"thinking", "Working", " idle"} {
		state, gotRaw := NormalizeState(raw)
		if state == StateIdle {
			t.Fatalf("NormalizeState(%q) folded to idle — the dangerous direction", raw)
		}
		if state != StateUnknown {
			t.Fatalf("NormalizeState(%q) = %q, want unknown", raw, state)
		}
		if gotRaw != raw {
			t.Fatalf("NormalizeState(%q) raw = %q, want the original spelling preserved", raw, gotRaw)
		}
	}
}

// TestEraseLineage_RemovesOnlyNamedLane (ADR 0096 decision 6).
func TestEraseLineage_RemovesOnlyNamedLane(t *testing.T) {
	withTempState(t)
	RecordBirth(Birth{Name: "keep1", Kind: "claude", Origin: OriginUser})
	RecordBirth(Birth{Name: "del1", Kind: "claude", Origin: OriginUser})
	if err := EraseLineage("del1"); err != nil {
		t.Fatal(err)
	}
	page, err := BuildPage(0, clockNow().Add(time.Hour).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	births := lineageOf(t, page, "birth")
	if len(births) != 1 || births[0]["name"] != "keep1" {
		t.Fatalf("want only keep1 to survive erasure, got %v", births)
	}
}

// TestRecordInstruct_TruncatesExcerpt (ADR 0096 decision 4: ≤140 chars, single line).
func TestRecordInstruct_TruncatesExcerpt(t *testing.T) {
	withTempState(t)
	long := ""
	for i := 0; i < 40; i++ {
		long += "0123456789"
	}
	multiline := "line one\nline two " + long
	RecordInstruct("conv:x", "s1", "operator", multiline)
	page, err := BuildPage(0, clockNow().Add(time.Hour).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Activity) != 1 {
		t.Fatalf("want 1 activity event, got %d", len(page.Activity))
	}
	b, _ := json.Marshal(page.Activity[0])
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	excerpt := m["excerpt"].(string)
	if r := []rune(excerpt); len(r) > 140 {
		t.Fatalf("excerpt is %d runes, want <=140", len(r))
	}
	for _, c := range excerpt {
		if c == '\n' {
			t.Fatalf("excerpt still contains a newline: %q", excerpt)
		}
	}
}

// setLastLineageTs rewrites the most recently appended lineage.jsonl line's ts field, so
// tests can place events at specific instants without threading a clock through every
// Record* call (those always stamp "now"). Kept in the test file: production code never
// rewrites the append-only ledger outside EraseLineage.
func setLastLineageTs(t *testing.T, ts string) {
	t.Helper()
	rewriteLastLine(t, lineagePath(), ts)
}

func setClock(t *testing.T, ts string) {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t.Fatal(err)
	}
	old := clockNow
	clockNow = func() time.Time { return tm }
	t.Cleanup(func() { clockNow = old })
}

func rewriteLastLine(t *testing.T, path, ts string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := splitLines(b)
	if len(lines) == 0 {
		t.Fatalf("no lines in %s", path)
	}
	var m map[string]any
	if err := json.Unmarshal(lines[len(lines)-1], &m); err != nil {
		t.Fatal(err)
	}
	m["ts"] = ts
	nb, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	lines[len(lines)-1] = nb
	var out []byte
	for _, l := range lines {
		out = append(out, l...)
		out = append(out, '\n')
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			if i > start {
				out = append(out, b[start:i])
			}
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

func resetLastStateForTest() {
	stateMu.Lock()
	defer stateMu.Unlock()
	lastState = map[string]LedgerState{}
}
