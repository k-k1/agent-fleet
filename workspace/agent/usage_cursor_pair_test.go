package main

// A cursor turn is two ledger rows: the fold's measured=none call and the stop hook's
// tokens-only row. The hook measured the turn, so it must not also count as unmeasured (#2495).

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

func cursorFoldRow(day, ref string, idx int) usagex.Record {
	r := at(row("fold-"+ref+"-"+string(rune('a'+idx)), usagex.FeatureSession, session.KindCursor, "", 0), day)
	r.Ref, r.Idx, r.Measured = ref, idx, usagex.MeasuredNone
	return r
}

func cursorHook(day, ref, gen string) usagex.Record {
	r := hookRow(day, gen)
	r.Ref = ref
	return r
}

func TestCursorTurnMeasuredByHookIsNotUnmeasured(t *testing.T) {
	day := daysAgo(0)
	tests := []struct {
		name          string
		rows          []usagex.Record
		calls, unmeas int
	}{
		{"hook measured the turn", []usagex.Record{cursorFoldRow(day, "s1", 1), cursorHook(day, "s1", "g1")}, 1, 0},
		{"hook never fired", []usagex.Record{cursorFoldRow(day, "s1", 1)}, 1, 1},
		{"two turns, one hook", []usagex.Record{
			cursorFoldRow(day, "s1", 1), cursorFoldRow(day, "s1", 2), cursorHook(day, "s1", "g1")}, 2, 1},
		{"hook of another session", []usagex.Record{cursorFoldRow(day, "s1", 1), cursorHook(day, "s2", "g1")}, 1, 1},
		{"redelivered stop counts once", []usagex.Record{
			cursorFoldRow(day, "s1", 1), cursorFoldRow(day, "s1", 2),
			cursorHook(day, "s1", "g1"), cursorHook(day, "s1", "g1")}, 2, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useIsolatedUsageDir(t)
			writeUsageDay(t, day, tc.rows...)
			got := getSeries(t, "from="+day+"&to="+day)
			if got.Totals.Calls != tc.calls || got.UnmeasuredCalls != tc.unmeas {
				t.Fatalf("calls=%d unmeasured=%d, want %d / %d", got.Totals.Calls, got.UnmeasuredCalls, tc.calls, tc.unmeas)
			}
		})
	}
}

// Other kinds' rows are never re-labelled: they write no tokens-only rows, and a cursor hook
// row of the same ref name does not reach a row of another kind.
func TestPairCursorHookTurnsLeavesOtherKindsAlone(t *testing.T) {
	for _, kind := range []string{session.KindClaude, session.KindCodex, session.KindOpencode, session.KindKiro, session.KindAgy} {
		t.Run(kind, func(t *testing.T) {
			fold := cursorFoldRow(daysAgo(0), "s1", 1)
			fold.Kind = kind
			hook := cursorHook(daysAgo(0), "s1", "g1")
			rows := []usagex.Record{fold, hook}
			pairCursorHookTurns(rows)
			if rows[0].Measured != usagex.MeasuredNone {
				t.Fatalf("%s fold row re-labelled to %q", kind, rows[0].Measured)
			}
			agg := aggregateUsageRows(rows, map[string]bool{})
			unmeasured := 0
			for k, a := range agg {
				if k.Measured == usagex.MeasuredNone {
					unmeasured += a.Calls
				}
			}
			if unmeasured != 1 {
				t.Fatalf("%s unmeasured = %d, want 1", kind, unmeasured)
			}
		})
	}
}
