package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// A quiet agent's mark drops its per-event ids (#3081) without giving up the guarantees of
// #3070: each row once, nothing lost from a late change, a replayable interrupted batch.

var (
	longAgo = time.Now().Add(-3 * usageAgentQuiet)
	justNow = time.Now()
)

// foldAgentAt is foldAgent with an explicit file mtime and agent id.
func foldAgentAt(t *testing.T, m session.Meta, id, stamp string, mod time.Time, turns []transcript.Turn) int {
	t.Helper()
	usageFoldMu.Lock()
	defer usageFoldMu.Unlock()
	st := readUsageFoldState()
	n, dirty, err := foldSubagentUsage(m, &st, []sessionx.SubagentSource{
		{ID: id, Stamp: stamp, ModTime: mod, Turns: func() ([]transcript.Turn, error) { return turns, nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	if dirty {
		if err := writeUsageFoldState(st); err != nil {
			t.Fatal(err)
		}
	}
	return n
}

func agentMarkOf(m session.Meta, id string) usageAgentMark {
	usageFoldMu.Lock()
	defer usageFoldMu.Unlock()
	return readUsageFoldState().Subagents[subagentMarkKey(m, id)]
}

func countedSums(m session.Meta) (in, out int) {
	idx := usageDedupIndex{}
	for _, r := range usagex.ReadRows() {
		if r.Ref == m.Name && idx.accept(r, rowEnd(r.TS)) {
			in += r.In
			out += r.Out
		}
	}
	return
}

func concat(parts ...[]transcript.Turn) []transcript.Turn {
	var all []transcript.Turn
	for _, p := range parts {
		all = append(all, p...)
	}
	return all
}

func TestRetiredAgentMarkKeepsNoIDsAndFoldsOnce(t *testing.T) {
	useTempUsageDir(t)
	m := session.Meta{Name: "retire3081", Kind: session.KindClaude, Model: "claude-opus-5"}
	const id = "/subagents/agent-r.jsonl"
	turns := concat(agentTurn("a", "2026-10-30T09:00:00Z", 2000, 20), agentTurn("b", "2026-10-30T09:05:00Z", 2500, 30))
	if n := foldAgentAt(t, m, id, "s1", justNow, turns); n != 2 {
		t.Fatalf("first fold = %d rows", n)
	}
	if mk := agentMarkOf(m, id); mk.Total != nil || len(mk.Turns) != 2 {
		t.Fatalf("a recent agent must keep its ids: %+v", mk)
	}
	// The same file, now quiet for long enough.
	if n := foldAgentAt(t, m, id, "s1", longAgo, turns); n != 0 {
		t.Fatalf("retiring wrote %d rows", n)
	}
	mk := agentMarkOf(m, id)
	if mk.Total == nil || len(mk.Turns) != 0 || mk.Total.In != 4500 || mk.Total.Out != 50 || mk.Seq != 2 {
		t.Fatalf("mark not retired to its totals: %+v", mk)
	}
	// Repeated folds, with and without a changed stamp, add nothing.
	for _, stamp := range []string{"s1", "s2", "s2"} {
		if n := foldAgentAt(t, m, id, stamp, longAgo, turns); n != 0 {
			t.Fatalf("fold %s of an unchanged agent = %d rows", stamp, n)
		}
	}
	if in, out := countedSums(m); in != 4500 || out != 50 {
		t.Fatalf("counted in=%d out=%d, want 4500 / 50", in, out)
	}
}

func TestRetiredAgentMarkFoldsLateAppendsAndCopiesExactly(t *testing.T) {
	useTempUsageDir(t)
	m := session.Meta{Name: "retirelate3081", Kind: session.KindClaude, Model: "claude-opus-5"}
	const id = "/subagents/agent-l.jsonl"
	a := agentTurn("a", "2026-10-30T09:00:00Z", 2000, 20)
	c := agentTurn("c", "2026-10-30T09:10:00Z", 3000, 5)
	foldAgentAt(t, m, id, "s1", longAgo, concat(a, c))
	foldAgentAt(t, m, id, "s1", longAgo, concat(a, c)) // retire
	if agentMarkOf(m, id).Total == nil {
		t.Fatal("not retired")
	}
	// A late copy puts an older turn in front.
	b := agentTurn("b", "2026-10-30T09:05:00Z", 2600, 30)
	if n := foldAgentAt(t, m, id, "s2", longAgo, concat(a, b, c)); n != 1 {
		t.Fatalf("late older turn = %d rows, want 1", n)
	}
	// The newest turn grows, and a new one follows.
	grown := agentTurn("c", "2026-10-30T09:10:00Z", 3000, 5)
	grown = append(grown, transcript.Turn{Role: "assistant", Model: "claude-opus-5", InTok: 3000, OutTok: 7,
		TS: "2026-10-30T09:11:00Z", Sidechain: true, AnchorID: "c2"})
	d := agentTurn("d", "2026-10-30T09:20:00Z", 4000, 11)
	if n := foldAgentAt(t, m, id, "s3", longAgo, concat(a, b, grown, d)); n != 1 {
		t.Fatalf("growth + new turn = %d rows, want 1 difference row", n)
	}
	// What the transcript holds now, once: in = 2000+2600+3000+4000, out = 20+30+(5+7)+11.
	if in, out := countedSums(m); in != 11600 || out != 73 {
		t.Fatalf("counted in=%d out=%d, want 11600 / 73", in, out)
	}
	if n := foldAgentAt(t, m, id, "s4", longAgo, concat(a)); n != 0 {
		t.Fatalf("a shorter read wrote %d rows", n)
	}
	if n := foldAgentAt(t, m, id, "s5", longAgo, concat(a, b, grown, d)); n != 0 {
		t.Fatalf("the full read again wrote %d rows", n)
	}
	if in, out := countedSums(m); in != 11600 || out != 73 {
		t.Fatalf("after the shorter read: in=%d out=%d", in, out)
	}
}

// A read that lacks a recorded turn and has new growth nets the two in a sum. It must not be
// written (the growth would be lost for good) nor its stamp kept: the full read that follows
// folds the growth exactly.
func TestRetiredAgentMarkHoldsAReadThatLacksARecordedTurn(t *testing.T) {
	useTempUsageDir(t)
	m := session.Meta{Name: "retirehold3081", Kind: session.KindClaude, Model: "claude-opus-5"}
	const id = "/subagents/agent-h.jsonl"
	a := agentTurn("a", "2026-10-30T09:00:00Z", 2000, 20)
	c := agentTurn("c", "2026-10-30T09:10:00Z", 3000, 5)
	d := agentTurn("d", "2026-10-30T09:20:00Z", 1500, 10)
	foldAgentAt(t, m, id, "s1", longAgo, concat(a, c))
	foldAgentAt(t, m, id, "s1", longAgo, concat(a, c)) // retire
	if n := foldAgentAt(t, m, id, "s2", longAgo, concat(c, d)); n != 0 {
		t.Fatalf("a read lacking turn a wrote %d rows", n)
	}
	if mk := agentMarkOf(m, id); mk.Stamp != "s1" || mk.Total == nil || mk.Total.In != 5000 {
		t.Fatalf("held mark changed: %+v", mk)
	}
	if n := foldAgentAt(t, m, id, "s3", longAgo, concat(a, c, d)); n != 1 {
		t.Fatalf("the copy returned: %d rows, want the growth once", n)
	}
	if in, out := countedSums(m); in != 6500 || out != 35 {
		t.Fatalf("counted in=%d out=%d, want 6500 / 35", in, out)
	}
}

// Only an agent quiet for the whole usageAgentQuiet is retired.
func TestAgentMarkRetiresOnlyAfterTheQuietPeriod(t *testing.T) {
	m := session.Meta{Name: "retireedge3081", Kind: session.KindClaude, Model: "claude-opus-5"}
	for _, tc := range []struct {
		age     time.Duration
		retired bool
	}{{usageAgentQuiet - time.Minute, false}, {usageAgentQuiet + time.Minute, true}} {
		useTempUsageDir(t)
		const id = "/subagents/agent-e.jsonl"
		turns := agentTurn("a", "2026-10-30T09:00:00Z", 2000, 20)
		mod := time.Now().Add(-tc.age)
		foldAgentAt(t, m, id, "s1", mod, turns)
		foldAgentAt(t, m, id, "s1", mod, turns)
		if got := agentMarkOf(m, id).Total != nil; got != tc.retired {
			t.Fatalf("quiet for %v: retired = %v, want %v", tc.age, got, tc.retired)
		}
	}
}

func TestRetiredAgentMarkReplaysAnInterruptedBatch(t *testing.T) {
	useTempUsageDir(t)
	m := session.Meta{Name: "retirecrash3081", Kind: session.KindClaude, Model: "claude-opus-5"}
	const id = "/subagents/agent-c.jsonl"
	a := agentTurn("a", "2026-10-30T09:00:00Z", 2000, 20)
	b := agentTurn("b", "2026-10-30T09:06:00Z", 2600, 30)
	c := agentTurn("c", "2026-10-30T09:12:00Z", 3000, 5)
	foldAgentAt(t, m, id, "s1", longAgo, a)
	foldAgentAt(t, m, id, "s1", longAgo, a) // retire
	if n := foldAgentAt(t, m, id, "s2", longAgo, concat(a, b)); n != 1 {
		t.Fatalf("growth fold = %d rows", n)
	}
	// The append landed but the batch was never cleared from the saved mark.
	rows := usagex.ReadRows()
	last := rows[len(rows)-1]
	usageFoldMu.Lock()
	st := readUsageFoldState()
	k := subagentMarkKey(m, id)
	mk := st.Subagents[k]
	retired := mk.Total != nil
	mk.Pending = []usagex.Record{last}
	st.Subagents[k] = mk
	werr := writeUsageFoldState(st)
	usageFoldMu.Unlock()
	if werr != nil {
		t.Fatal(werr)
	}
	if !retired {
		t.Fatal("mark lost its retired form")
	}
	if n := foldAgentAt(t, m, id, "s3", longAgo, concat(a, b, c)); n != 1 {
		t.Fatalf("fold after the interruption = %d rows, want the 1 new difference", n)
	}
	if in, out := countedSums(m); in != 7600 || out != 55 {
		t.Fatalf("counted in=%d out=%d, want 7600 / 55", in, out)
	}
}

// A state.json written before retirement existed (ids, no total) still loads, folds without a
// duplicate, and retires once quiet.
func TestStateWrittenBeforeRetirementStillLoads(t *testing.T) {
	useTempUsageDir(t)
	m := session.Meta{Name: "retirecompat3081", Kind: session.KindClaude, Model: "claude-opus-5"}
	const id = "/subagents/agent-o.jsonl"
	turns := concat(agentTurn("a", "2026-10-30T09:00:00Z", 2000, 20), agentTurn("b", "2026-10-30T09:05:00Z", 2500, 30))
	foldAgentAt(t, m, id, "s1", justNow, turns)
	b, err := os.ReadFile(usageStatePath())
	if err != nil {
		t.Fatal(err)
	}
	// The format as shipped: an "ids" array on every turn, no "total".
	if !strings.Contains(string(b), `"ids"`) || strings.Contains(string(b), `"total"`) {
		t.Fatalf("unexpected state shape:\n%s", b)
	}
	if n := foldAgentAt(t, m, id, "s1", longAgo, turns); n != 0 {
		t.Fatalf("old mark re-folded %d rows", n)
	}
	if mk := agentMarkOf(m, id); mk.Total == nil || mk.Total.In != 4500 {
		t.Fatalf("old mark did not retire: %+v", mk)
	}
	if in, out := countedSums(m); in != 4500 || out != 50 {
		t.Fatalf("counted in=%d out=%d", in, out)
	}
}

// state.json no longer grows with the number of subagent events: many agents of many turns, all
// quiet, leave a file whose size does not depend on the turns.
func TestStateSizeIsBoundedOverManyTurns(t *testing.T) {
	size := func(turnsPerAgent int, mod time.Time) int64 {
		useTempUsageDir(t)
		m := session.Meta{Name: "retiresize3081", Kind: session.KindClaude, Model: "claude-opus-5"}
		for a := 0; a < 20; a++ {
			var turns []transcript.Turn
			for i := 0; i < turnsPerAgent; i++ {
				turns = append(turns, agentTurn(fmt.Sprintf("a%d-e%d-%036d", a, i, i), "2026-10-30T09:00:00Z", 1000+i, 10)...)
			}
			id := fmt.Sprintf("/subagents/agent-%d.jsonl", a)
			foldAgentAt(t, m, id, "s1", mod, turns)
			foldAgentAt(t, m, id, "s1", mod, turns)
		}
		fi, err := os.Stat(usageStatePath())
		if err != nil {
			t.Fatal(err)
		}
		return fi.Size()
	}
	if live := size(200, justNow); live < 100_000 {
		t.Fatalf("control: recent agents should still hold their ids, state is %d bytes", live)
	}
	few, many := size(5, longAgo), size(200, longAgo)
	if many > few+2_000 {
		t.Fatalf("state grew with turns: %d bytes at 5 turns/agent, %d at 200", few, many)
	}
}
