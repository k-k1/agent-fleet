package chatx

// #1257: an operator instruction queued in a Managed session is not reported as done while its
// prompt is being sent or waits, and is reported as not run when its prompt is dropped before it
// ran.

import (
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// queueBehindATurn queues an operator prompt for row behind a running turn of session name, as
// a Managed driver's accept does, and returns the queue and the running turn.
func queueBehindATurn(t *testing.T, name, row string) (*agents.TurnQueue, *agents.Taken) {
	t.Helper()
	q := agents.NewTurnQueue(name, nil, agents.LedgerAtAccept)
	q.Accept(agents.TurnInput{Prompt: "member", ClientMessageID: "m0", Origin: agents.Origin{Kind: agents.OriginMember}})
	run := q.Take()
	q.Commit(run)
	q.Received(run)
	q.Accept(agents.TurnInput{Prompt: "do it", Instr: row, Origin: agents.Origin{Kind: agents.OriginOperator}})
	if !agents.HeldInstrs(name)[row] {
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

	id := addInstructionAt(m.Name, conv, "operator", time.Now().Add(-60*time.Second))
	q, run := queueBehindATurn(t, m.Name, id)
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
	if tk == nil || tk.In.Instr != id || !q.Commit(tk) {
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

// sendingRow raises a row whose send this process has under way, delivered at.
func sendingRow(t *testing.T, name, conv string, at time.Time) string {
	t.Helper()
	return addSendingInstructionAt(name, conv, "operator", instrBoot, at)
}

// Review round 1, finding 4: a row raised before the send is out of the settle decision until
// the send's outcome, so a send the driver refuses never leaves a delivered report behind.
func TestReportReconcilerSendingInstructionIsNotDone(t *testing.T) {
	m, sid, conv := ledgerFixture(t, "slot74")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	id := sendingRow(t, m.Name, conv, time.Now().Add(-60*time.Minute))
	status.PersistTurnEnd(sid, "idle")
	for i := 0; i < 4; i++ {
		clock.advance(t, rc, reportTickDefault)
	}
	if cs.count() != 0 {
		t.Fatalf("an instruction still being sent was reported: %v", cs.calls)
	}
	WithdrawInstruction(m.Name, id) // the driver refused it
	if rows := ReadInstrRows(m.Name); len(rows) != 0 {
		t.Fatalf("rows after the withdrawal = %+v", rows)
	}
	sent := AddSendingInstruction(m.Name, conv, "operator")
	MarkInstrSent(m.Name, sent)
	for _, r := range ReadInstrRows(m.Name) {
		if r.ID == sent && r.Sending != "" {
			t.Fatal("MarkInstrSent left the row sending")
		}
	}
}

// Review round 2, finding 3: a row left sending by an Agent that is gone. Without a held file
// nothing shows whether the prompt reached the session: it is reported as unconfirmed, once,
// and never as done, however long an earlier turn's quiet evidence lasts. With a held file the
// driver had accepted it: the row is an ordinary queued instruction again.
func TestReportReconcilerSendingRowOfADeadAgent(t *testing.T) {
	m, sid, conv := ledgerFixture(t, "slot75")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	lost := addSendingInstructionAt(m.Name, conv, "operator", "boot-gone", time.Now().Add(-6*time.Minute))
	accepted := addSendingInstructionAt(m.Name, conv, "operator", "boot-gone", time.Now().Add(-6*time.Minute))
	queueBehindATurn(t, m.Name, accepted)
	status.PersistTurnEnd(sid, "idle")
	for i := 0; i < 4; i++ {
		clock.advance(t, rc, reportTickDefault)
	}
	if got := cs.callsSnapshot(); len(got) != 1 || got[0] != reportKindUnconfirmed+":" || cs.rowIDs(0)[0] != lost {
		t.Fatalf("reports = %v %v, want one unconfirmed for the lost row", got, cs.rows)
	}
	for _, r := range ReadInstrRows(m.Name) {
		switch r.ID {
		case lost:
			if r.State != instrUnconfirmed {
				t.Fatalf("lost row = %+v", r)
			}
		case accepted:
			if r.State != instrPending || r.Sending != "" {
				t.Fatalf("accepted row = %+v, want pending and no longer sending", r)
			}
		}
	}
}

// A dropped prompt's instruction is reported as not run, with the drop's reason, even after the
// session's meta is gone (trash), and never afterwards as done. A failed delivery is retried.
// A drop during the send (the row still sending) is reported too.
func TestReportReconcilerDroppedInstructionIsReportedNotRun(t *testing.T) {
	m, sid, conv := ledgerFixture(t, "slot71")
	cs := countingSink{fail: 1}
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	id := sendingRow(t, m.Name, conv, time.Now().Add(-60*time.Second))
	queueBehindATurn(t, m.Name, id)
	if MarkInstrNotRun(m.Name, "nope", agents.DropArchived) {
		t.Fatal("marked a row that does not exist")
	}
	agents.DropHeld(m.Name, agents.DropTrashed) // what the trash does; no hook installed here
	session.RemoveMeta(m.Name)
	if !MarkInstrNotRun(m.Name, id, agents.DropTrashed) {
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
	MarkInstrSent(m.Name, id) // the send returns after the drop: nothing reopens
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
	a := AddInstruction(m.Name, conv, "operator")
	b := AddInstruction(m.Name, conv, "operator")
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

// A send that fails withdraws the row it was given, and only that row.
func TestWithdrawInstruction(t *testing.T) {
	m, _, conv := ledgerFixture(t, "slot73")
	keep := AddInstruction(m.Name, conv, "operator")
	id := AddSendingInstruction(m.Name, conv, "operator")
	WithdrawInstruction(m.Name, id)
	rows := ReadInstrRows(m.Name)
	if len(rows) != 1 || rows[0].ID != keep {
		t.Fatalf("rows after the withdrawal = %+v", rows)
	}
}

// Review round 3: a sweep that lands anywhere in this process's own send — the row just
// raised, the prompt accepted and already started but MarkInstrSent not yet written, or a
// refusal about to withdraw it — reports nothing. The row names its sender's boot id, so a
// live send is never taken for a gone Agent's, whatever the interleaving; the completion is
// still reported once the send has settled.
func TestReportReconcilerOwnSendIsNeverUnconfirmed(t *testing.T) {
	m, sid, conv := ledgerFixture(t, "slot76")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)
	status.PersistTurnEnd(sid, "idle")
	sweep := func(what string) {
		t.Helper()
		for i := 0; i < 3; i++ {
			clock.advance(t, rc, reportTickDefault)
		}
		if cs.count() != 0 {
			t.Fatalf("%s: reported %v", what, cs.callsSnapshot())
		}
	}

	refused := AddSendingInstruction(m.Name, conv, "operator")
	sweep("row just raised")
	WithdrawInstruction(m.Name, refused)
	sweep("after a refusal")

	id := sendingRow(t, m.Name, conv, time.Now().Add(-60*time.Second))
	q, run := queueBehindATurn(t, m.Name, id)
	q.Settle(run)
	tk := q.Take()
	if !q.Commit(tk) { // started: no held file, MarkInstrSent not written yet
		t.Fatal("commit refused")
	}
	sweep("accepted and started, send not yet settled")

	MarkInstrSent(m.Name, id)
	waitPastCursor(t, openInstrRows(m.Name)[0].Cursor.At)
	status.PersistTurnEnd(sid, "idle")
	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault)
	if got := cs.callsSnapshot(); len(got) != 1 || got[0] != ReportKindAnswerReady+":" || cs.rowIDs(0)[0] != id {
		t.Fatalf("after the send settled: %v", got)
	}
}
