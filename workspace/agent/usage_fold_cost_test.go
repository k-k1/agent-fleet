package main

import (
	"fmt"
	"math"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

func asstCost(model string, in, out, read int, cost float64) transcript.Turn {
	t := asst(model, in, out, read, 0, false)
	t.CostUSD = cost
	return t
}

// opencode reports each message's own cost (one LLM call each). A turn's cost is the sum of
// its messages, it lands in cost_usd only, and re-folding never adds it twice.
func TestFoldOpencodeReportedCost(t *testing.T) {
	useTempUsageDir(t)
	const model = "big-pickle"
	turns := []transcript.Turn{
		{Role: "user", Text: "1"},
		asstCost(model, 5858, 41, 1913, 0.0187629), // a tool-call step
		asstCost(model, 55, 11, 7771, 0.0026613),   // the answer
		{Role: "user", Text: "compaction"},
		asstCost(model, 900, 120, 0, 0.00552), // the compaction summary is a billed call too
		{Role: "user", Text: "3"},
		asstCost(model, 100, 10, 0, 0.001), // still open
	}
	m := session.Meta{Name: "oc01", Kind: session.KindOpencode}
	fold := func() int {
		t.Helper()
		usageFoldMu.Lock()
		defer usageFoldMu.Unlock()
		st := readUsageFoldState()
		n, err := foldSessionUsageWithTurns(m, &st, turns, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeUsageFoldState(st); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := fold(); n != 2 {
		t.Fatalf("first pass = %d rows, want 2", n)
	}
	if n := fold(); n != 0 {
		t.Fatalf("re-fold added %d rows", n)
	}
	rows := usagex.ReadRows()
	if len(rows) != 2 {
		t.Fatalf("ledger = %d rows, want 2", len(rows))
	}
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-12 }
	if !near(rows[0].CostUSD, 0.0187629+0.0026613) || !near(rows[1].CostUSD, 0.00552) {
		t.Fatalf("cost_usd = %v / %v, want the per-message sums 0.0214242 / 0.00552", rows[0].CostUSD, rows[1].CostUSD)
	}
	// Tokens keep their own rule (output summed, input replaced): the cost is not derived from them.
	if rows[0].Out != 52 || rows[0].In != 55 {
		t.Fatalf("tokens = in %d out %d, want in 55 out 52", rows[0].In, rows[0].Out)
	}
	agg := aggregateUsageRows(rows, map[string]bool{})
	var total float64
	for _, a := range agg {
		total += a.CostUSD
	}
	if !near(total, 0.0187629+0.0026613+0.00552) {
		t.Fatalf("aggregate cost_usd = %v, want each message counted once", total)
	}
}

// A kind that reports no cost keeps an empty cost_usd.
func TestFoldWithoutReportedCostLeavesCostEmpty(t *testing.T) {
	useTempUsageDir(t)
	turns := []transcript.Turn{
		{Role: "user", Text: "1"},
		asst("claude-haiku-4-5", 100, 10, 0, 20, false),
		{Role: "user", Text: "2"},
	}
	st := readUsageFoldState()
	if _, err := foldSessionUsageWithTurns(session.Meta{Name: "c01", Kind: session.KindClaude}, &st, turns, false); err != nil {
		t.Fatal(err)
	}
	if rows := usagex.ReadRows(); len(rows) != 1 || rows[0].CostUSD != 0 {
		t.Fatalf("rows = %+v, want one row with no cost_usd", rows)
	}
}

func costOnly(cost float64, sidechain bool, ts string) transcript.Turn {
	return costOnlyID(fmt.Sprintf("msg_%v_%v_%s", cost, sidechain, ts), cost, sidechain, ts)
}

func costOnlyID(id string, cost float64, sidechain bool, ts string) transcript.Turn {
	return transcript.Turn{Role: "assistant", CostOnly: true, CostUSD: cost, Sidechain: sidechain, TS: ts, AnchorID: id}
}

func foldOnce(t *testing.T, m session.Meta, turns []transcript.Turn, includeTrailing bool) int {
	t.Helper()
	usageFoldMu.Lock()
	defer usageFoldMu.Unlock()
	st := readUsageFoldState()
	n, err := foldSessionUsageWithTurns(m, &st, turns, includeTrailing)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeUsageFoldState(st); err != nil {
		t.Fatal(err)
	}
	return n
}

// The session ends on a billed call with nothing to display, with no assistant turn after it:
// the settle on archive/delete still records its cost, once — main and sidechain alike.
func TestFoldTrailingCostOnlySettles(t *testing.T) {
	useTempUsageDir(t)
	m := session.Meta{Name: "oc02", Kind: session.KindOpencode, Model: "opencode/big-pickle"}
	turns := []transcript.Turn{
		{Role: "user", Text: "1"},
		costOnly(0.5, false, "2026-10-04T00:00:01Z"),
		costOnly(0.125, true, "2026-10-04T00:00:02Z"),
	}
	if n := foldOnce(t, m, turns, false); n != 0 {
		t.Fatalf("open segment folded %d rows, want 0", n)
	}
	if n := foldOnce(t, m, turns, true); n != 2 {
		t.Fatalf("settle = %d rows, want 2 (main + sidechain)", n)
	}
	if n := foldOnce(t, m, turns, true); n != 0 {
		t.Fatalf("second settle added %d rows", n)
	}
	rows := usagex.ReadRows()
	if len(rows) != 2 || rows[0].CostUSD != 0.5 || rows[0].Sidechain || rows[1].CostUSD != 0.125 || !rows[1].Sidechain {
		t.Fatalf("rows = %+v, want 0.5 main + 0.125 sidechain", rows)
	}
	for _, r := range rows {
		if r.Idx != 0 || r.Spend != 0 {
			t.Fatalf("cost-only row = %+v, want idx 0 and no tokens", r)
		}
	}
	if mark := readUsageFoldState().Sessions[m.Name]; mark.Groups != 0 || len(mark.OrphanKeys) != 2 {
		t.Fatalf("watermark = %+v, want groups 0 / two orphan keys", mark)
	}
	if rows[0].Key == "" || rows[0].Key == rows[1].Key {
		t.Fatalf("keys = %q / %q, want two distinct ledger keys", rows[0].Key, rows[1].Key)
	}
}

// Cost-only turns never open, close or split a logical turn: the logical turns and their
// numbering are exactly what they are without them, and each cost lands once.
func TestFoldCostOnlyKeepsLogicalTurns(t *testing.T) {
	const model = "big-pickle"
	plain := []transcript.Turn{
		{Role: "user", Text: "1"},
		asstCost(model, 100, 10, 0, 1),
		asstCost(model, 120, 5, 0, 1),
		{Role: "user", Text: "2"},
		asstCost(model, 200, 20, 0, 1),
		{Role: "user", Text: "3"},
	}
	with := []transcript.Turn{
		{Role: "user", Text: "1"},
		costOnly(0.5, false, ""), // before the turn's first shown reply: joins it
		asstCost(model, 100, 10, 0, 1),
		costOnly(0.25, true, ""), // a subagent's empty call mid-turn: must not split the turn
		asstCost(model, 120, 5, 0, 1),
		costOnly(0.125, false, ""), // inside the open turn: joins it
		{Role: "user", Text: "2"},
		costOnly(4, false, ""), // a turn with nothing shown: its own cost-only row
		{Role: "user", Text: "2b"},
		asstCost(model, 200, 20, 0, 1),
		{Role: "user", Text: "3"},
	}
	a, b := foldTurnRows(plain, false), foldTurnRows(with, false)
	var logical []usageTurnRow
	var orphan []usageTurnRow
	for _, r := range b {
		if r.Orphan {
			orphan = append(orphan, r)
		} else {
			logical = append(logical, r)
		}
	}
	if len(logical) != len(a) {
		t.Fatalf("logical turns = %d, want %d", len(logical), len(a))
	}
	for i := range a {
		if logical[i].Idx != a[i].Idx || logical[i].Tokens != a[i].Tokens {
			t.Fatalf("turn %d = %+v, want %+v", i, logical[i], a[i])
		}
	}
	if logical[0].CostUSD != 2.625 || logical[1].CostUSD != 1 {
		t.Fatalf("turn costs = %v / %v, want 2.625 / 1", logical[0].CostUSD, logical[1].CostUSD)
	}
	if len(orphan) != 2 || orphan[0].CostUSD != 0.25 || !orphan[0].Sidechain || orphan[1].CostUSD != 4 || orphan[0].OrphanKey == "" || orphan[0].OrphanKey == orphan[1].OrphanKey {
		t.Fatalf("cost-only rows = %+v, want sidechain 0.25 then main 4", orphan)
	}
	if got, want := sessionx.AggregateUsage(with).Cumulative, sessionx.AggregateUsage(plain).Cumulative; got != want {
		t.Fatalf("get_session_usage with cost-only turns = %+v, want %+v", got, want)
	}
}

// A watermark written before cost-only rows existed: the logical turns are not folded again,
// and the cost-only rows are taken once.
func TestFoldCostOnlyAgainstOldWatermark(t *testing.T) {
	useTempUsageDir(t)
	m := session.Meta{Name: "oc03", Kind: session.KindOpencode}
	turns := []transcript.Turn{
		{Role: "user", Text: "1"},
		asstCost("big-pickle", 100, 10, 0, 1),
		{Role: "user", Text: "2"},
		{Role: "user", Text: "3"},
	}
	if n := foldOnce(t, m, turns, false); n != 1 {
		t.Fatalf("first fold = %d, want 1", n)
	}
	turns = []transcript.Turn{
		{Role: "user", Text: "1"},
		asstCost("big-pickle", 100, 10, 0, 1),
		{Role: "user", Text: "2"},
		costOnly(0.5, false, "2026-10-04T00:00:01Z"),
		{Role: "user", Text: "3"},
	}
	if n := foldOnce(t, m, turns, false); n != 1 {
		t.Fatalf("fold with the cost-only call = %d, want 1 (only it)", n)
	}
	if n := foldOnce(t, m, turns, false); n != 0 {
		t.Fatalf("re-fold added %d", n)
	}
	var total float64
	for _, r := range usagex.ReadRows() {
		total += r.CostUSD
	}
	if total != 1.5 {
		t.Fatalf("total cost_usd = %v, want 1.5", total)
	}
}

// The same session moves to another conversation (a new one, a relaunch that could not resume):
// its cost-only rows are told apart by message id, not by count or time, so the new one folds.
func TestFoldCostOnlyAfterConversationSwap(t *testing.T) {
	useTempUsageDir(t)
	m := session.Meta{Name: "oc04", Kind: session.KindOpencode}
	a := []transcript.Turn{{Role: "user", Text: "1"}, costOnlyID("msg_a", 0.5, false, "2026-10-04T00:00:01Z"), {Role: "user", Text: "2"}}
	b := []transcript.Turn{{Role: "user", Text: "1"}, costOnlyID("msg_b", 0.75, false, "2026-10-04T01:00:01Z"), {Role: "user", Text: "2"}}
	if n := foldOnce(t, m, a, false); n != 1 {
		t.Fatalf("conversation A = %d rows, want 1", n)
	}
	if n := foldOnce(t, m, b, false); n != 1 {
		t.Fatalf("conversation B = %d rows, want 1 (its cost-only call is new)", n)
	}
	if n := foldOnce(t, m, b, false); n != 0 {
		t.Fatalf("re-fold of B added %d", n)
	}
	if n := foldOnce(t, m, a, false); n != 0 {
		t.Fatalf("swapping back to A added %d", n)
	}
	var total float64
	for _, r := range usagex.ReadRows() {
		total += r.CostUSD
	}
	if total != 1.25 {
		t.Fatalf("total cost_usd = %v, want 1.25", total)
	}
}

// The append succeeded and the watermark write did not: the next pass re-appends the cost-only
// row, and the aggregation still counts its cost once (Key dedup).
func TestFoldCostOnlyCrashWindowIsNotDoubleCounted(t *testing.T) {
	useIsolatedUsageDir(t)
	m := session.Meta{Name: "oc05", Kind: session.KindOpencode}
	turns := []transcript.Turn{
		{Role: "user", Text: "1"},
		asstCost("big-pickle", 100, 10, 0, 1),
		{Role: "user", Text: "2"},
		costOnlyID("msg_e", 0.5, false, "2026-07-26T00:00:00Z"),
		{Role: "user", Text: "3"},
	}
	fold := func(persist bool) {
		t.Helper()
		usageFoldMu.Lock()
		defer usageFoldMu.Unlock()
		st := readUsageFoldState()
		if _, err := foldSessionUsageWithTurns(m, &st, turns, false); err != nil {
			t.Fatal(err)
		}
		if persist {
			if err := writeUsageFoldState(st); err != nil {
				t.Fatal(err)
			}
		}
	}
	fold(false) // rows written, died before the watermark
	fold(true)  // the next pass appends both again
	if n := len(usagex.ReadRows()); n != 4 {
		t.Fatalf("ledger = %d rows, want 4 (the crash window is not reproduced)", n)
	}
	day := "2026-07-26"
	got := getSeries(t, "from="+day+"&to="+day)
	if math.Abs(got.Totals.CostUSD-1.5) > 1e-12 {
		t.Fatalf("totals cost_usd = %v, want 1.5 (each counted once)", got.Totals.CostUSD)
	}
}
