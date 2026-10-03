package chatx

// #1257: an operator instruction queued in a Managed session is not reported as done while its
// prompt waits, and is reported as not run when its prompt is dropped before it ran.

import (
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// queueBehindATurn queues an operator prompt under msg behind a running turn of session name,
// as a Managed driver's accept does, and returns the queue and the running turn.
func queueBehindATurn(t *testing.T, name, msg string) (*agents.TurnQueue, *agents.Taken) {
	t.Helper()
	q := agents.NewTurnQueue(name, nil, agents.LedgerAtAccept)
	q.Accept(agents.TurnInput{Prompt: "member", ClientMessageID: "m0", Origin: agents.Origin{Kind: agents.OriginMember}})
	run := q.Take()
	q.Commit(run)
	q.Received(run)
	q.Accept(agents.TurnInput{Prompt: "do it", ClientMessageID: msg, Origin: agents.Origin{Kind: agents.OriginOperator}})
	if !agents.HeldWaiting(name, msg) {
		t.Fatal("the queued operator prompt is not held")
	}
	return q, run
}

// The turn the prompt queued behind ends (or is stopped, or the session halts): the session is
// quiet after the instruction's cursor, yet the instruction has not started. It stays pending
// until its prompt has run.
func TestReportReconcilerHeldInstructionIsNotDone(t *testing.T) {
	m, sid, conv := ledgerFixture(t, "slot70")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	q, run := queueBehindATurn(t, m.Name, "o1")
	id := addQueuedInstructionAt(m.Name, conv, "operator", "o1", time.Now().Add(-60*time.Second))
	status.PersistTurnEnd(sid, "idle") // the earlier turn ended after the instruction arrived
	for i := 0; i < 4; i++ {
		clock.advance(t, rc, reportTickDefault)
	}
	if cs.count() != 0 {
		t.Fatalf("an instruction whose prompt never started was reported: %v", cs.calls)
	}

	// The prompt is handed to the runtime and its turn ends: now it is done.
	q.Settle(run)
	tk := q.Take()
	if tk == nil || tk.ID() != "o1" || !q.Commit(tk) {
		t.Fatalf("the operator prompt did not start: %+v", tk)
	}
	open := openInstrRows(m.Name)
	waitPastCursor(t, open[0].Cursor.At)
	status.PersistTurnEnd(sid, "idle")
	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 1 || cs.calls[0] != ReportKindAnswerReady+":" || cs.rowIDs(0)[0] != id {
		t.Fatalf("the instruction's completion = %v %v", cs.calls, cs.rows)
	}
}

// A dropped prompt's instruction is reported as not run, with the drop's reason, even after the
// session's meta is gone (trash), and never afterwards as done. A failed delivery is retried.
func TestReportReconcilerDroppedInstructionIsReportedNotRun(t *testing.T) {
	m, sid, conv := ledgerFixture(t, "slot71")
	cs := countingSink{fail: 1}
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	queueBehindATurn(t, m.Name, "o1")
	id := addQueuedInstructionAt(m.Name, conv, "operator", "o1", time.Now().Add(-60*time.Second))
	if MarkInstrNotRun(m.Name, "nope", agents.DropArchived) {
		t.Fatal("marked a row for a prompt it does not have")
	}
	agents.DropHeld(m.Name, agents.DropTrashed) // what the trash does; no hook installed here
	session.RemoveMeta(m.Name)
	if !MarkInstrNotRun(m.Name, "o1", agents.DropTrashed) {
		t.Fatal("the row was not marked")
	}
	clock.waitSweep(t, rc) // the mark's wake-up: delivery fails once
	clock.advance(t, rc, reportTickDefault)
	want := reportKindNotRun + ":" + agents.DropTrashed
	if got := cs.callsSnapshot(); len(got) != 2 || got[0] != want || got[1] != want || cs.rowIDs(1)[0] != id {
		t.Fatalf("not-run deliveries = %v", got)
	}
	rows := ReadInstrRows(m.Name)
	if len(rows) != 1 || rows[0].State != instrNotRun || SessionReportPending(m.Name) {
		t.Fatalf("row after the not-run report = %+v", rows)
	}
	status.PersistTurnEnd(sid, "idle")
	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault)
	if n := cs.count(); n != 2 {
		t.Fatalf("a not-run instruction was reported again: %v", cs.callsSnapshot())
	}
	if c := instrReopenCandidates(ReadInstrRows(m.Name), time.Now(), reportReopenGrace); len(c) != 0 {
		t.Fatalf("a not-run row is a reopen candidate: %+v", c)
	}
}

// The real sink writes a not-run card: its own key, the reason, and no "completion of N
// instructions" note.
func TestNotRunReportCard(t *testing.T) {
	m, _, conv := ledgerFixture(t, "slot72")
	a := addQueuedInstructionAt(m.Name, conv, "operator", "o1", time.Now())
	b := addQueuedInstructionAt(m.Name, conv, "operator", "o2", time.Now())
	var rows []instrRow
	for _, r := range ReadInstrRows(m.Name) {
		if r.ID == a || r.ID == b {
			rows = append(rows, r)
		}
	}
	if res := recordSessionReport(m.Name, conv, reportKindNotRun, agents.DropDiscarded, rows); res != reportSinkOK {
		t.Fatalf("sink = %v", res)
	}
	c, err := LoadConv(conv)
	if err != nil {
		t.Fatal(err)
	}
	card := c.Messages[len(c.Messages)-1]
	if card.NoticeKey != reportKeyNotRun || card.ReportReason != agents.DropDiscarded || card.NoticeArgs["fold_n"] != "" {
		t.Fatalf("card = %+v", card)
	}
	if !strings.Contains(card.Content, "停止でキューが破棄された") {
		t.Fatalf("content = %q", card.Content)
	}
	prompt := ReportPromptFor(card, "en")
	if !strings.Contains(prompt, "did not run") || !strings.Contains(prompt, "send_to_session") {
		t.Fatalf("prompt = %q", prompt)
	}
}

// A send that fails withdraws the row it was given.
func TestWithdrawInstruction(t *testing.T) {
	m, _, conv := ledgerFixture(t, "slot73")
	keep := AddInstruction(m.Name, conv, "operator")
	id := AddQueuedInstruction(m.Name, conv, "operator", "o1")
	WithdrawInstruction(m.Name, id)
	rows := ReadInstrRows(m.Name)
	if len(rows) != 1 || rows[0].ID != keep {
		t.Fatalf("rows after the withdrawal = %+v", rows)
	}
}
