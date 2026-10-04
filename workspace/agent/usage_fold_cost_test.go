package main

import (
	"math"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
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
