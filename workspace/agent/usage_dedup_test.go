package main

// Regression for (ref, idx) deduplication (usage_dedup.go / docs/log/46 §7-4).
//
// One gap to close: when folding dies between appending the rows and writing the watermark, the
// next pass re-appends several turns of that session. The writer cannot close it (the two are
// separate files and cannot be written atomically), so what is checked here is that aggregation
// drops them.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// sessionRow builds one folded row. Even a duplicate row gets a different call id (the folder
// mints a UUID per row), so deduplicating on call never catches it and (ref, idx) is needed.
func sessionRow(call, ref string, idx int, day string, spend int) usagex.Record {
	r := at(row(call, usagex.FeatureSession, session.KindClaude, "claude-haiku-4-5", spend), day)
	r.Ref, r.Idx = ref, idx
	return r
}

// Drops a re-append inside the same raw file (fold-on-read running right after a crash).
func TestUsageSeriesDropsDuplicateSessionRows(t *testing.T) {
	useIsolatedUsageDir(t)
	day := daysAgo(0)
	writeUsageDay(t, day,
		sessionRow("c1", "slot01", 1, day, 100),
		sessionRow("c2", "slot01", 2, day, 200),
		// crashed here (watermark not updated) → the next pass re-appends the same two turns
		// and writes what follows too
		sessionRow("c3", "slot01", 1, day, 100),
		sessionRow("c4", "slot01", 2, day, 200),
		sessionRow("c5", "slot01", 3, day, 300),
	)
	got := getSeries(t, "from="+day+"&to="+day)
	if got.Totals.Spend != 600 || got.Totals.Calls != 3 {
		t.Fatalf("totals = %+v, want spend 600 / calls 3 (1200 / 5 without dedup)", got.Totals)
	}
	// The duplicate rows must stay in the raw ledger: only aggregation drops them, so the rows
	// remain available for an after-the-fact audit.
	if n := len(usagex.ReadRows()); n != 5 {
		t.Fatalf("raw = %d rows, want 5 (rows must never be removed from the ledger)", n)
	}
}

// When the crash window crosses midnight the duplicate lands in a different file day than the
// original. Rollup runs per file, so unless the watermark is carried across files the
// consumption is counted twice.
func TestRollupDropsDuplicatesAcrossFileDays(t *testing.T) {
	useIsolatedUsageDir(t)
	d2, d1 := daysAgo(2), daysAgo(1)
	writeUsageDay(t, d2,
		sessionRow("c1", "slot01", 1, d2, 100),
		sessionRow("c2", "slot01", 2, d2, 200),
	)
	// The next day's pass re-appends idx 2 (the consumption time comes from the transcript, so
	// the consumption day stays d2).
	writeUsageDay(t, d1,
		sessionRow("c3", "slot01", 2, d2, 200),
		sessionRow("c4", "slot01", 3, d1, 300),
	)
	ensureUsageRollups()

	sum := func(day string) (spend, calls int) {
		for _, e := range readUsageRollup(day[:7]).Days[day].Entries {
			spend += e.Agg.Spend
			calls += e.Agg.Calls
		}
		return spend, calls
	}
	if spend, calls := sum(d2); spend != 300 || calls != 2 {
		t.Fatalf("consumption day %s = spend %d / calls %d, want 300 / 2 (500 / 3 without dedup)", d2, spend, calls)
	}
	if spend, calls := sum(d1); spend != 300 || calls != 1 {
		t.Fatalf("consumption day %s = spend %d / calls %d, want 300 / 1", d1, spend, calls)
	}
}

// The original is already rolled up (the rollup is authoritative and the raw is not read) while
// only the duplicate sits in a raw file that has not been rolled up. Unless the watermark is
// carried over from the rollup state, nothing can drop this duplicate.
func TestDedupSpansRolledAndRawBoundary(t *testing.T) {
	useIsolatedUsageDir(t)
	yesterday, today := daysAgo(1), daysAgo(0)
	writeUsageDay(t, yesterday,
		sessionRow("c1", "slot01", 1, yesterday, 100),
		sessionRow("c2", "slot01", 2, yesterday, 200),
	)
	ensureUsageRollups() // yesterday is rolled up and its (ref,idx) watermark stays in state
	writeUsageDay(t, today,
		sessionRow("c3", "slot01", 2, yesterday, 200), // re-appended (consumption day is yesterday)
		sessionRow("c4", "slot01", 3, today, 300),
	)

	day := getSeries(t, "from="+yesterday+"&to="+today)
	if day.Totals.Spend != 600 || day.Totals.Calls != 3 {
		t.Fatalf("bucket=day totals = %+v, want spend 600 / calls 3", day.Totals)
	}
	// bucket=hour ignores the rollup and re-reads every raw row, rebuilding the watermark from
	// empty, so check that it did not drop the original as well (its total must match day's).
	hour := getSeries(t, "from="+yesterday+"&to="+today+"&bucket=hour")
	if hour.Totals.Spend != 600 || hour.Totals.Calls != 3 {
		t.Fatalf("bucket=hour totals = %+v, want spend 600 / calls 3", hour.Totals)
	}
}

// Only duplicates may be dropped. Losing a row is worse than a duplicate (that consumption never
// comes back), so every shape that merely looks like a duplicate has to pass.
func TestUsageDedupKeepsLegitimateRows(t *testing.T) {
	dd := usageDedupIndex{}
	ts := func(min int) time.Time { return time.Date(2026, 7, 26, 0, min, 0, 0, time.UTC) }
	accept := func(ref string, idx int, min int) bool {
		return dd.accept(sessionRow("c", ref, idx, "2026-07-26", 10), ts(min))
	}
	if !accept("slot01", 1, 1) || !accept("slot01", 2, 2) {
		t.Fatal("dropped consecutively numbered turns")
	}
	if !accept("slot02", 1, 3) {
		t.Fatal("dropped another session's identical idx (ref is not being looked at)")
	}
	if accept("slot01", 2, 2) {
		t.Fatal("let a duplicate through")
	}
	if !accept("slot01", 3, 4) {
		t.Fatal("dropped the new turn that follows a duplicate")
	}
	// An auxiliary call has no idx, so however many identically shaped rows arrive, none is dropped.
	aux := at(row("c9", usagex.FeatureTitleSession, session.KindClaude, "haiku", 10), "2026-07-26")
	aux.Ref = "slot01"
	if !dd.accept(aux, ts(5)) || !dd.accept(aux, ts(5)) {
		t.Fatal("treated auxiliary-call rows as duplicates (with no idx there is nothing to decide on)")
	}
	// Slug reuse: a deleted session's name is handed out again. idx goes back to 1, but the
	// consumption time is always later — dropping this makes the new session's consumption
	// vanish silently.
	if !accept("slot01", 1, 99) {
		t.Fatal("treated idx=1 after slug reuse as a duplicate")
	}
}

// On a version bump, a rollup that may already have folded duplicates in is rebuilt from raw.
// The aggregate is a sum and cannot be subtracted from, so rebuilding is the only way to drop it.
func TestRollupRebuildPurgesLegacyDuplicates(t *testing.T) {
	useIsolatedUsageDir(t)
	day := daysAgo(1)
	writeUsageDay(t, day,
		sessionRow("c1", "slot01", 1, day, 100),
		sessionRow("c2", "slot01", 1, day, 100), // a duplicate that slipped in back in v1
	)
	// Place a v1 rollup (an aggregate with the duplicate folded in) and a state with no version.
	k := usageKey{Feature: usagex.FeatureSession, Trigger: usagex.TriggerUser, Kind: session.KindClaude,
		Model: "claude-haiku-4-5", ModelSrc: usagex.ModelReported, Measured: usagex.MeasuredExact, OK: true}
	if err := writeUsageJSON(usageRollupPath(day[:7]), usageRollupMonth{Days: map[string]usageRollupDay{
		day: {Src: []string{day}, Entries: []usageRollupEntry{{Key: k, Agg: usageAgg{Spend: 200, In: 200, Calls: 2}}}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := writeUsageJSON(usageRollupStatePath(), map[string]any{
		"rolled": map[string]usageRolledFile{day: {MinDay: day, MaxDay: day}},
	}); err != nil {
		t.Fatal(err)
	}

	ensureUsageRollups()

	entries := readUsageRollup(day[:7]).Days[day].Entries
	if len(entries) != 1 || entries[0].Agg.Spend != 100 || entries[0].Agg.Calls != 1 {
		t.Fatalf("aggregate after the rebuild = %+v, want spend 100 / calls 1", entries)
	}
	if v := readUsageRollupState().Version; v != usageRollupVersion {
		t.Fatalf("version is still %d (the rebuild would be attempted on every pass)", v)
	}
}

// When the contributing raw file has been pruned, do not rebuild: losing the aggregate for what
// is gone costs more. Instead the watermark is restored from the rolled-up files still visible,
// so duplicates arriving later can still be dropped.
func TestRollupRebuildKeepsDataWhenRawIsPruned(t *testing.T) {
	dir := useIsolatedUsageDir(t)
	gone, kept := daysAgo(3), daysAgo(2)
	writeUsageDay(t, gone, sessionRow("c0", "slot99", 1, gone, 700))
	writeUsageDay(t, kept, sessionRow("c1", "slot01", 1, kept, 100))
	ensureUsageRollups()
	if err := os.Remove(filepath.Join(dir, "raw", gone+".jsonl")); err != nil {
		t.Fatal(err)
	}
	// Wind the version back so it looks like a v1 rollup is still lying around.
	st := readUsageRollupState()
	st.Version, st.Folded = 1, nil
	if err := writeUsageJSON(usageRollupStatePath(), st); err != nil {
		t.Fatal(err)
	}

	ensureUsageRollups()

	if e := readUsageRollup(gone[:7]).Days[gone].Entries; len(e) != 1 || e[0].Agg.Spend != 700 {
		t.Fatalf("lost the aggregate for the day whose raw file is gone: %+v", e)
	}
	// The watermark is restored from the raw files that remain, so later duplicates are dropped.
	writeUsageDay(t, daysAgo(1), sessionRow("c2", "slot01", 1, kept, 100))
	ensureUsageRollups()
	spend := 0
	for _, e := range readUsageRollup(kept[:7]).Days[kept].Entries {
		spend += e.Agg.Spend
	}
	if spend != 100 {
		t.Fatalf("the restored watermark did not drop the duplicate: spend = %d, want 100", spend)
	}
}

// Reproduces the real crash window end to end: fold → die before the watermark is written → the
// next pass re-appends the same turns. The ledger holds them twice; the series counts them once.
func TestFoldCrashWindowIsNotDoubleCounted(t *testing.T) {
	useIsolatedUsageDir(t)
	turns := []transcript.Turn{
		{Role: "user", Text: "1"},
		asst("claude-haiku-4-5", 100, 10, 0, 0, false),
		{Role: "user", Text: "2"},
		asst("claude-haiku-4-5", 200, 20, 0, 0, false),
		{Role: "user", Text: "3"}, // closes the preceding turn
	}
	m := session.Meta{Name: "slot01", Kind: session.KindClaude}
	fold := func(persist bool) {
		t.Helper()
		usageFoldMu.Lock()
		defer usageFoldMu.Unlock()
		st := readUsageFoldState()
		if _, err := foldSessionUsageWithTurns(m, &st, turns, false); err != nil {
			t.Fatalf("fold failed: %v", err)
		}
		if persist {
			if err := writeUsageFoldState(st); err != nil {
				t.Fatal(err)
			}
		}
	}
	fold(false) // the rows were written, then it died before the watermark
	fold(true)  // the next pass re-appends the same two turns

	if n := len(usagex.ReadRows()); n != 4 {
		t.Fatalf("ledger = %d rows, want 4 (the double-entry crash window is not reproduced)", n)
	}
	want := 0 // one logical turn each (counting the duplicates would double it)
	for _, r := range foldTurnRows(turns, false) {
		want += usagex.Spend(r.Tokens.In, r.Tokens.CacheCreate, r.Tokens.Out)
	}
	day := "2026-07-26" // asst()'s TS (the consumption day)
	got := getSeries(t, "from="+day+"&to="+day)
	if got.Totals.Spend != want || got.Totals.Calls != 2 {
		t.Fatalf("totals = %+v, want spend %d / calls 2", got.Totals, want)
	}
}

// The hashed key exists so that a ref is never left in cleartext in a store with no expiry
// (ADR0029 §8: no ref in the rollup). Check that no session name appears in the index file.
func TestUsageDedupIndexDoesNotStoreRefNames(t *testing.T) {
	useIsolatedUsageDir(t)
	day := daysAgo(1)
	writeUsageDay(t, day, sessionRow("c1", "slot-secret", 1, day, 100))
	ensureUsageRollups()
	b, err := os.ReadFile(usageRollupStatePath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "slot-secret") {
		t.Fatalf("the index carries a ref in cleartext: %s", b)
	}
	if len(readUsageRollupState().Folded) != 1 {
		t.Fatalf("no watermark was recorded: %+v", readUsageRollupState().Folded)
	}
}
