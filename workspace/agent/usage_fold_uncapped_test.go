package main

import (
	"fmt"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// lineTurns is n user/assistant exchanges with absolute line indices and rising timestamps,
// the way the claude reader numbers them.
func lineTurns(n int) []transcript.Turn {
	var out []transcript.Turn
	for i := 0; i < n; i++ {
		ts := fmt.Sprintf("2026-07-26T00:%02d:00Z", i)
		a := asst("claude-haiku-4-5", 100, 10, 0, 0, false)
		a.TS = ts
		out = append(out, transcript.Turn{Role: "user", Idx: 2 * i, TS: ts}, a)
		out[len(out)-1].Idx = 2*i + 1
	}
	return out
}

// A session folded while the reader hid its oldest turns must not fold again once they are
// visible: the uncapped numbering is shifted by the hidden count, which put the newest turns
// past the old watermark.
func TestFoldAfterCapRemovedDoesNotRecount(t *testing.T) {
	useTempUsageDir(t)
	m := session.Meta{Name: "cl01", Kind: session.KindClaude}
	all := lineTurns(12) // 12 logical turns, the last still open
	capped := all[16:]   // the reader saw only the newest 4 logical turns
	fold := func(turns []transcript.Turn) int {
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
	if n := fold(capped); n != 3 {
		t.Fatalf("capped pass = %d rows, want 3", n)
	}
	// The capped pass above was written by this build, which marks it uncapped; a mark from
	// before the change lacks the flag.
	usageFoldMu.Lock()
	st := readUsageFoldState()
	mk := st.Sessions[m.Name]
	mk.Uncapped = false
	st.Sessions[m.Name] = mk
	if err := writeUsageFoldState(st); err != nil {
		t.Fatal(err)
	}
	usageFoldMu.Unlock()
	if n := fold(all); n != 0 {
		t.Fatalf("uncapped pass re-appended %d rows", n)
	}
	if n := len(usagex.ReadRows()); n != 3 {
		t.Fatalf("ledger = %d rows, want 3", n)
	}
	// Turns appended afterwards are still folded, once.
	grown := lineTurns(14)
	if n := fold(grown); n != 2 {
		t.Fatalf("growth pass = %d rows, want 2", n)
	}
	if n := fold(grown); n != 0 {
		t.Fatalf("re-fold added %d rows", n)
	}
}
