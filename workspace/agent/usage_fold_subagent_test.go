package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// The ledger folds a claude session's subagent and Workflow-agent transcripts (#3054), each as
// its own row sequence: a session whose watermark predates the change gets each agent row once,
// its own rows are not renumbered, and repeated or out-of-order flushes add nothing twice.
func TestFoldSubagentTranscriptsOnceEach(t *testing.T) {
	useTempUsageDir(t)
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	born := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return born.Add(d).Format(time.RFC3339Nano) }
	m := session.Meta{Name: "sub3054", Dir: t.TempDir(), Kind: session.KindClaude, Model: "claude-opus-5",
		CreatedAt: born.Format(time.RFC3339)}
	sid := session.UUID(m.Dir, m.Name)
	proj := filepath.Join(cfg, "projects", "p1")
	write := func(p, body string, appendTo bool) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		if appendTo {
			flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
		}
		f, err := os.OpenFile(p, flag, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(body); err != nil {
			t.Fatal(err)
		}
	}
	main := filepath.Join(proj, sid+".jsonl")
	agent := filepath.Join(proj, sid, "subagents", "agent-a1.jsonl")
	wf := filepath.Join(proj, sid, "subagents", "workflows", "wf_x", "agent-b1.jsonl")
	write(main, claudeUserLine(at(0))+claudeUsageLine(at(time.Minute), "r1", 1000, 10), false)
	write(agent, claudeUserLine(at(2*time.Minute))+claudeUsageLine(at(3*time.Minute), "a1", 2000, 20)+
		claudeUserLine(at(4*time.Minute))+claudeUsageLine(at(5*time.Minute), "a2", 2000, 30), false)
	write(wf, claudeUserLine(at(2*time.Minute))+claudeUsageLine(at(150*time.Second), "b1", 0, 40), false)

	fold := func() (int, bool) {
		t.Helper()
		usageFoldMu.Lock()
		defer usageFoldMu.Unlock()
		st := readUsageFoldState()
		n, dirty, err := foldSessionUsageLocked(m, &st, true)
		if err != nil {
			t.Fatal(err)
		}
		if dirty {
			if err := writeUsageFoldState(st); err != nil {
				t.Fatal(err)
			}
		}
		return n, dirty
	}
	sidechain := func() (rows int, spend int) {
		for _, r := range usagex.ReadRows() {
			if r.Ref == m.Name && r.Sidechain {
				rows++
				spend += r.Spend
			}
		}
		return
	}

	// Upgrade: the session's own transcript was folded before subagents were, with a mark from
	// that build (no subagent marks, none of the new flags).
	usageFoldMu.Lock()
	st := readUsageFoldState()
	if _, err := foldSessionUsageWithTurns(m, &st, sessionx.UsageTurns(m), true); err != nil {
		t.Fatal(err)
	}
	mk := st.Sessions[m.Name]
	mk.Uncapped = false
	st.Sessions[m.Name] = mk
	if err := writeUsageFoldState(st); err != nil {
		t.Fatal(err)
	}
	usageFoldMu.Unlock()
	if n := len(usagex.ReadRows()); n != 1 {
		t.Fatalf("legacy ledger = %d rows, want 1", n)
	}

	if n, _ := fold(); n != 3 {
		t.Fatalf("first fold after the upgrade = %d rows, want 3 (two agent turns + one Workflow turn)", n)
	}
	if rows, spend := sidechain(); rows != 3 || spend != (2000+20)+(2000+30)+40 {
		t.Fatalf("sidechain rows = %d spend = %d", rows, spend)
	}
	if n, dirty := fold(); n != 0 || dirty {
		t.Fatalf("re-fold = %d rows, dirty=%v; want nothing", n, dirty)
	}

	// Both agents grow in the same pass, the Workflow agent's file written first: each new turn
	// is folded once, and nothing already folded comes back.
	write(wf, claudeUserLine(at(6*time.Minute))+claudeUsageLine(at(7*time.Minute), "b2", 0, 50), true)
	write(agent, claudeUserLine(at(8*time.Minute))+claudeUsageLine(at(9*time.Minute), "a3", 2000, 60), true)
	if n, _ := fold(); n != 2 {
		t.Fatalf("fold after growth = %d rows, want 2", n)
	}
	if n, _ := fold(); n != 0 {
		t.Fatalf("re-fold after growth = %d rows", n)
	}
	if rows, spend := sidechain(); rows != 5 || spend != (2020+2030+40)+50+(2000+60) {
		t.Fatalf("sidechain rows = %d spend = %d after growth", rows, spend)
	}

	// The reader's dedup counts each row once and keeps one entry per agent, not per row; the
	// agents' Idx sequences overlap (both start at 1) and must not drop one another.
	idx := usageDedupIndex{}
	kept := 0
	for _, r := range usagex.ReadRows() {
		if idx.accept(r, rowEnd(r.TS)) {
			kept++
		}
	}
	if kept != 6 {
		t.Fatalf("dedup kept %d of 6 rows", kept)
	}
	if len(idx) != 3 { // the session's own sequence + two agents
		t.Fatalf("dedup entries = %d, want 3", len(idx))
	}
	for _, r := range usagex.ReadRows() {
		if idx.accept(r, rowEnd(r.TS)) {
			t.Fatalf("a replayed row %q idx %d was counted again", r.Agent, r.Idx)
		}
	}
}

func agentTurn(id, ts string, in, out int) []transcript.Turn {
	return []transcript.Turn{
		{Role: "user", TS: ts, Sidechain: true},
		{Role: "assistant", Model: "claude-opus-5", InTok: in, OutTok: out, TS: ts, Sidechain: true, AnchorID: id},
	}
}

// foldAgent folds one synthetic agent transcript the way the production pass does.
func foldAgent(t *testing.T, m session.Meta, stamp string, turns []transcript.Turn) int {
	t.Helper()
	usageFoldMu.Lock()
	defer usageFoldMu.Unlock()
	st := readUsageFoldState()
	n, dirty, err := foldSubagentUsage(m, &st, []sessionx.SubagentSource{
		{ID: "/subagents/agent-a.jsonl", Stamp: stamp, Turns: func() ([]transcript.Turn, error) { return turns, nil }},
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

func sidechainSums(m session.Meta) (rows, in, out int) {
	for _, r := range usagex.ReadRows() {
		if r.Ref == m.Name && r.Sidechain {
			rows++
			in += r.In
			out += r.Out
		}
	}
	return
}

// Nothing in an agent's file says it is finished, so its newest turn is folded as it stands.
// When that turn grows later (a long tool run, an approval wait) only the difference is added:
// the ledger ends at the same totals as one fold of the finished turn.
func TestFoldSubagentTurnThatGrowsAfterItWasFolded(t *testing.T) {
	useTempUsageDir(t)
	m := session.Meta{Name: "grow3054", Kind: session.KindClaude, Model: "claude-opus-5"}
	a := agentTurn("a", "2026-10-30T09:00:00Z", 2000, 20)
	// The same logical turn, a second assistant event six minutes on: input is the newest
	// context snapshot, output adds up.
	b := transcript.Turn{Role: "assistant", Model: "claude-opus-5", InTok: 2600, OutTok: 30,
		TS: "2026-10-30T09:06:00Z", Sidechain: true, AnchorID: "b"}
	if n := foldAgent(t, m, "s1", a); n != 1 {
		t.Fatalf("first fold = %d rows, want 1", n)
	}
	grown := append(append([]transcript.Turn{}, a...), b)
	if n := foldAgent(t, m, "s2", grown); n != 1 {
		t.Fatalf("fold after the turn grew = %d rows, want the 1 difference", n)
	}
	if n := foldAgent(t, m, "s2", grown); n != 0 {
		t.Fatalf("unchanged file folded %d rows", n)
	}
	if n := foldAgent(t, m, "s3", grown); n != 0 { // restamped, no new content
		t.Fatalf("restamped file folded %d rows", n)
	}
	if rows, in, out := sidechainSums(m); rows != 2 || in != 2600 || out != 50 {
		t.Fatalf("ledger rows=%d in=%d out=%d, want 2 / 2600 / 50 (one fold of the whole turn)", rows, in, out)
	}
	// A new turn after it closes the grown one: no further change to it.
	next := append(append([]transcript.Turn{}, grown...), agentTurn("c", "2026-10-30T09:10:00Z", 3000, 5)...)
	if n := foldAgent(t, m, "s4", next); n != 1 {
		t.Fatalf("fold with a new turn = %d rows, want 1", n)
	}
	if rows, in, out := sidechainSums(m); rows != 3 || in != 5600 || out != 55 {
		t.Fatalf("ledger rows=%d in=%d out=%d, want 3 / 5600 / 55", rows, in, out)
	}
}

// A copy of the agent found later can put an older turn in front of those already recorded; the
// recorded ones keep their identity, the older one is folded once.
func TestFoldSubagentOlderTurnFromALaterCopy(t *testing.T) {
	useTempUsageDir(t)
	m := session.Meta{Name: "copy3054", Kind: session.KindClaude, Model: "claude-opus-5"}
	older := agentTurn("old", "2026-10-30T09:00:00Z", 1000, 10)
	late := agentTurn("late", "2026-10-30T09:05:00Z", 2000, 20)
	if n := foldAgent(t, m, "s1", late); n != 1 {
		t.Fatalf("first fold = %d rows", n)
	}
	merged := append(append([]transcript.Turn{}, older...), late...)
	if n := foldAgent(t, m, "s2", merged); n != 1 {
		t.Fatalf("fold with the older copy = %d rows, want only the older turn", n)
	}
	if n := foldAgent(t, m, "s2", merged); n != 0 {
		t.Fatalf("re-fold = %d rows", n)
	}
	if rows, in, out := sidechainSums(m); rows != 2 || in != 3000 || out != 30 {
		t.Fatalf("ledger rows=%d in=%d out=%d, want 2 / 3000 / 30", rows, in, out)
	}
}

// A batch whose append landed but whose clearing did not (a crash, a failed save) is replayed
// as it was: the turn grew in between, and a recomputed difference under the same Idx would
// have been accepted next to the first one.
func TestFoldSubagentReplaysAnInterruptedBatchAsItWas(t *testing.T) {
	useTempUsageDir(t)
	m := session.Meta{Name: "crash3054", Kind: session.KindClaude, Model: "claude-opus-5"}
	a := agentTurn("a", "2026-10-30T09:00:00Z", 2000, 20)
	b := transcript.Turn{Role: "assistant", Model: "claude-opus-5", InTok: 2600, OutTok: 30,
		TS: "2026-10-30T09:06:00Z", Sidechain: true, AnchorID: "b"}
	c := transcript.Turn{Role: "assistant", Model: "claude-opus-5", InTok: 3000, OutTok: 5,
		TS: "2026-10-30T09:12:00Z", Sidechain: true, AnchorID: "c"}
	foldAgent(t, m, "s1", a)
	grown := append(append([]transcript.Turn{}, a...), b)
	if n := foldAgent(t, m, "s2", grown); n != 1 {
		t.Fatalf("growth fold = %d rows", n)
	}
	// The append landed but the batch was never cleared from the saved mark.
	rows := usagex.ReadRows()
	last := rows[len(rows)-1]
	usageFoldMu.Lock()
	st := readUsageFoldState()
	for k, mk := range st.Subagents {
		mk.Pending = []usagex.Record{last}
		st.Subagents[k] = mk
	}
	if err := writeUsageFoldState(st); err != nil {
		t.Fatal(err)
	}
	usageFoldMu.Unlock()
	// The turn grows again before the next pass.
	if n := foldAgent(t, m, "s3", append(append([]transcript.Turn{}, grown...), c)); n != 1 {
		t.Fatalf("fold after the interruption = %d rows, want the 1 new difference", n)
	}
	idx := usageDedupIndex{}
	in, out := 0, 0
	for _, r := range usagex.ReadRows() {
		if r.Ref == m.Name && idx.accept(r, rowEnd(r.TS)) {
			in += r.In
			out += r.Out
		}
	}
	if in != 3000 || out != 55 {
		t.Fatalf("counted in=%d out=%d, want 3000 / 55 (the whole turn once)", in, out)
	}
}

// A later copy that fills events into a turn already recorded, at either end, adds only what
// was missing and does not make a second turn.
func TestFoldSubagentLaterCopyFillsARecordedTurn(t *testing.T) {
	useTempUsageDir(t)
	m := session.Meta{Name: "fill3054", Kind: session.KindClaude, Model: "claude-opus-5"}
	ev := func(id string, in, out int) transcript.Turn {
		return transcript.Turn{Role: "assistant", Model: "claude-opus-5", InTok: in, OutTok: out,
			TS: "2026-10-30T09:00:00Z", Sidechain: true, AnchorID: id}
	}
	user := transcript.Turn{Role: "user", TS: "2026-10-30T09:00:00Z", Sidechain: true}
	// First copy: turn a holds one event, turn c one.
	first := []transcript.Turn{user, ev("a", 2000, 20), user, ev("c", 3000, 5)}
	if n := foldAgent(t, m, "s1", first); n != 2 {
		t.Fatalf("first fold = %d rows", n)
	}
	// Another copy adds an event inside a (after its first) and one in front of c.
	filled := []transcript.Turn{user, ev("a", 2000, 20), ev("b", 0, 30), user, ev("c0", 0, 7), ev("c", 3000, 5)}
	if n := foldAgent(t, m, "s2", filled); n != 2 {
		t.Fatalf("fold with the filled copy = %d rows, want 2 differences", n)
	}
	if n := foldAgent(t, m, "s2", filled); n != 0 {
		t.Fatalf("re-fold = %d rows", n)
	}
	// a: out 20+30; c: out 7+5 and the same input snapshot.
	if rows, in, out := sidechainSums(m); rows != 4 || in != 5000 || out != 62 {
		t.Fatalf("ledger rows=%d in=%d out=%d, want 4 / 5000 / 62", rows, in, out)
	}
}

// A batch saved before its append, where the append never happened, is written on the next pass
// instead of being lost with the totals already advanced.
func TestFoldSubagentWritesABatchSavedBeforeAnAppendThatFailed(t *testing.T) {
	useTempUsageDir(t)
	m := session.Meta{Name: "wal3054", Kind: session.KindClaude, Model: "claude-opus-5"}
	a := agentTurn("a", "2026-10-30T09:00:00Z", 2000, 20)
	foldAgent(t, m, "s1", a)
	grown := append(append([]transcript.Turn{}, a...), transcript.Turn{Role: "assistant", Model: "claude-opus-5",
		InTok: 2600, OutTok: 30, TS: "2026-10-30T09:06:00Z", Sidechain: true, AnchorID: "b"})
	lost := usageRecordOf(m, usageTurnRow{Idx: 2, TS: "2026-10-30T09:06:00Z", Sidechain: true,
		Tokens: usagex.Tokens{In: 600, Out: 30}}, usageAgentID("/subagents/agent-a.jsonl"))
	usageFoldMu.Lock()
	st := readUsageFoldState()
	for k, mk := range st.Subagents {
		mk.Seq = 2
		mk.Turns = []usageAgentTurn{{IDs: []string{usageAgentID("a")[:8]}, In: 2600, Out: 50}}
		mk.Pending = []usagex.Record{lost}
		st.Subagents[k] = mk
	}
	if err := writeUsageFoldState(st); err != nil {
		t.Fatal(err)
	}
	usageFoldMu.Unlock()
	foldAgent(t, m, "s2", grown)
	if rows, in, out := sidechainSums(m); rows != 2 || in != 2600 || out != 50 {
		t.Fatalf("ledger rows=%d in=%d out=%d, want 2 / 2600 / 50", rows, in, out)
	}
}

// The copy found later and the live file's growth meet in one pass: events are added in front
// of a recorded turn and after it, so neither its first nor its last event is the recorded one.
// Its own event is still inside, and it is matched by that.
func TestFoldSubagentMatchesATurnByAnyOfItsEvents(t *testing.T) {
	useTempUsageDir(t)
	m := session.Meta{Name: "both3054", Kind: session.KindClaude, Model: "claude-opus-5"}
	ev := func(id string, in, out int) transcript.Turn {
		return transcript.Turn{Role: "assistant", Model: "claude-opus-5", InTok: in, OutTok: out,
			TS: "2026-10-30T09:00:00Z", Sidechain: true, AnchorID: id}
	}
	user := transcript.Turn{Role: "user", TS: "2026-10-30T09:00:00Z", Sidechain: true}
	if n := foldAgent(t, m, "s1", []transcript.Turn{user, ev("a", 2000, 20)}); n != 1 {
		t.Fatalf("first fold = %d rows", n)
	}
	both := []transcript.Turn{user, ev("z", 0, 10), ev("a", 2000, 20), ev("b", 0, 30)}
	if n := foldAgent(t, m, "s2", both); n != 1 {
		t.Fatalf("fold with events at both ends = %d rows, want the 1 difference", n)
	}
	if rows, in, out := sidechainSums(m); rows != 2 || in != 2000 || out != 60 {
		t.Fatalf("ledger rows=%d in=%d out=%d, want 2 / 2000 / 60", rows, in, out)
	}
}
