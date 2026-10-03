package agents

import (
	"strings"
	"testing"
	"time"
)

func operator(id string) TurnInput {
	return TurnInput{Prompt: "operator " + id, ClientMessageID: id, Origin: Origin{Kind: OriginOperator}}
}

func scheduled(id string) TurnInput {
	return TurnInput{Prompt: "schedule " + id, ClientMessageID: id, Origin: Origin{Kind: OriginSchedule},
		Schedule: ScheduleRef{ID: "sch1", Slot: "2026-09-30T12:00:00Z"}}
}

// captureDrops installs OnHeldDropped for the test and returns what it was told.
func captureDrops(t *testing.T) *[]HeldDrop {
	t.Helper()
	var got []HeldDrop
	prev := OnHeldDropped
	OnHeldDropped = func(d HeldDrop) { got = append(got, d) }
	t.Cleanup(func() { OnHeldDropped = prev })
	return &got
}

func dropSummary(ds []HeldDrop) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.ID+":"+d.Reason)
	}
	return out
}

// #1257: operator and scheduled prompts queued behind a turn survive teardown like peer
// messages, and come back in one FIFO with them, in accept order, with their origin, their
// schedule and a queue-time mark. The member's own input is still not held.
func TestHeldOperatorAndScheduleSurviveTeardownInOneFIFO(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("m0"))
	running(t, q)
	q.Accept(peer("p1"))
	q.Accept(operator("o1"))
	q.Accept(member("m1"))
	q.Accept(scheduled("s1"))
	q.Accept(peer("p2"))
	if got := len(heldFiles(t, "tq")); got != 4 {
		t.Fatalf("held files = %d, want 4 (two peers, the operator and the schedule)", got)
	}
	q.DropAll()

	h := &queueHandle{q: NewTurnQueue("tq", q.ledger, LedgerAtAccept)}
	DeliverHeld("tq", h)
	DeliverHeld("tq", h)
	sameIDs(t, "queue after restart", h.q.Items(), "p1", "o1", "s1", "p2")
	items := h.q.Items()
	if items[1].Origin.Kind != OriginOperator || items[2].Origin.Kind != OriginSchedule {
		t.Fatalf("origins after restart = %+v", items)
	}
	at := q.now().Local().Format(time.RFC3339)
	if want := "operator o1\n\n[agent-fleet:held queued=" + at + "]"; items[1].Text != want {
		t.Fatalf("restored operator prompt = %q, want %q", items[1].Text, want)
	}
	if orig, ok := StripHeldMark(items[1].Text); !ok || orig != "operator o1" {
		t.Fatalf("StripHeldMark = %q, %v", orig, ok)
	}
	if !strings.HasPrefix(items[0].Text, "peer p1") {
		t.Fatalf("a peer prompt without an envelope changed: %q", items[0].Text)
	}
	tk := h.q.Take()
	h.q.Commit(tk)
	h.q.Settle(tk)
	tk = h.q.Take()
	if tk.ID() != "o1" {
		t.Fatalf("second take = %s", tk.ID())
	}
	h.q.Commit(tk)
	h.q.Settle(tk)
	tk = h.q.Take()
	if tk.In.Schedule != (ScheduleRef{ID: "sch1", Slot: "2026-09-30T12:00:00Z"}) {
		t.Fatalf("schedule ref lost across the restart: %+v", tk.In.Schedule)
	}
}

// HeldWaiting is true from accept until the entry is handed to the runtime, and across a
// teardown: what the report reconciler reads to keep an instruction that has not run pending.
func TestHeldWaitingUntilCommit(t *testing.T) {
	q := newQ(t, LedgerAtTake)
	q.Accept(member("m0"))
	running(t, q)
	q.Accept(operator("o1"))
	if !HeldWaiting("tq", "o1") {
		t.Fatal("a queued operator prompt is not waiting")
	}
	q.DropAll()
	if !HeldWaiting("tq", "o1") {
		t.Fatal("teardown released the operator prompt")
	}
	h := &queueHandle{q: NewTurnQueue("tq", q.ledger, LedgerAtTake)}
	DeliverHeld("tq", h)
	tk := h.q.Take()
	if !h.q.Commit(tk) {
		t.Fatal("commit refused")
	}
	if HeldWaiting("tq", "o1") {
		t.Fatal("still waiting after it was handed to the runtime")
	}
}

// Every way an operator or scheduled prompt goes without running is reported once, with the
// reason; a peer message's and the member's own are not.
func TestDroppedHeldInputsAreReported(t *testing.T) {
	drops := captureDrops(t)
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("m0"))
	m0 := running(t, q)
	q.Accept(operator("o1"))
	q.Accept(peer("p1"))
	q.Accept(member("m1"))
	q.Accept(scheduled("s1"))
	q.Accept(operator("o2"))
	if _, err := q.Remove("o2"); err != nil {
		t.Fatal(err)
	}
	q.Interrupt(InterruptOpts{DiscardQueue: true}, true)
	if got := strings.Join(dropSummary(*drops), " "); got != "o2:removed o1:discarded s1:discarded" {
		t.Fatalf("drops = %q", got)
	}
	if (*drops)[2].Schedule.ID != "sch1" || (*drops)[2].Origin != OriginSchedule {
		t.Fatalf("schedule drop = %+v", (*drops)[2])
	}
	if got := heldFiles(t, "tq"); len(got) != 0 {
		t.Fatalf("held files after the discard: %v", got)
	}

	// A first stop with nothing running stops the input whose start is in flight.
	*drops = nil
	q.Settle(m0)
	q.Accept(operator("o3"))
	q.Interrupt(InterruptOpts{}, false)
	if got := strings.Join(dropSummary(*drops), " "); got != "o3:stopped" {
		t.Fatalf("first-stop drops = %q", got)
	}

	// Archive and the like drop from disk, after a teardown.
	*drops = nil
	q.Accept(member("m2"))
	running(t, q)
	q.Accept(operator("o4"))
	q.Accept(peer("p2"))
	q.Accept(scheduled("s2"))
	q.DropAll()
	DropHeld("tq", DropArchived)
	if got := strings.Join(dropSummary(*drops), " "); got != "o4:archived s2:archived" {
		t.Fatalf("archive drops = %q", got)
	}
}

// The operator's stop_session withdraws its own held prompts only.
func TestDropHeldOriginKeepsTheRest(t *testing.T) {
	drops := captureDrops(t)
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("m0"))
	running(t, q)
	q.Accept(operator("o1"))
	q.Accept(peer("p1"))
	q.Accept(scheduled("s1"))
	q.DropAll()
	DropHeldOrigin("tq", OriginOperator, DropWithdrawn)
	if got := strings.Join(dropSummary(*drops), " "); got != "o1:withdrawn" {
		t.Fatalf("drops = %q", got)
	}
	h := &queueHandle{q: NewTurnQueue("tq", q.ledger, LedgerAtAccept)}
	DeliverHeld("tq", h)
	sameIDs(t, "after the withdrawal", h.q.Items(), "p1", "s1")
}

func TestMarkHeldInstruction(t *testing.T) {
	at := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	got := MarkHeldInstruction("do it\n\n[agent-fleet] call af_report", at)
	if MarkHeldInstruction(got, at) != got {
		t.Fatal("marked twice")
	}
	if orig, ok := StripHeldMark(got); !ok || orig != "do it\n\n[agent-fleet] call af_report" {
		t.Fatalf("strip = %q, %v", orig, ok)
	}
	if MarkHeldInstruction("x", time.Time{}) != "x" {
		t.Fatal("a zero time marked")
	}
	if s, ok := StripHeldMark("plain [agent-fleet:held queued=x] then more"); ok || s != "plain [agent-fleet:held queued=x] then more" {
		t.Fatal("stripped a mark that is not at the end")
	}
}

// Review round 1, finding 2: a drop and a queue that adopted the same held input race (an
// archive while a Resume delivers). Whoever removes the file owns the input: once a drop has
// reported it as not run the queue's Commit refuses it, and once Commit has handed it over the
// drop does not report it.
func TestHeldDropAndCommitClaimTheInputOnce(t *testing.T) {
	drops := captureDrops(t)
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("m0"))
	running(t, q)
	o1 := operator("o1")
	o1.Instr = "i-1"
	q.Accept(o1)
	q.DropAll()

	h := &queueHandle{q: NewTurnQueue("tq", q.ledger, LedgerAtAccept)}
	DeliverHeld("tq", h) // the Resume adopts it
	DropHeld("tq", DropArchived)
	if len(*drops) != 1 || (*drops)[0].Instr != "i-1" {
		t.Fatalf("drops = %+v", *drops)
	}
	tk := h.q.Take()
	if tk == nil || h.q.Commit(tk) {
		t.Fatal("a queue committed an input a drop had reported as not run")
	}
	if h.q.Head() != nil || h.q.Len() != 0 {
		t.Fatalf("the refused input is still queued: %+v", h.q.Items())
	}

	// The other order: committed first, then the drop finds nothing to report.
	*drops = nil
	o2 := operator("o2")
	o2.Instr = "i-2"
	h.q.Accept(member("m1"))
	running(t, h.q)
	h.q.Accept(o2)
	if !HeldInstrs("tq")["i-2"] {
		t.Fatal("HeldInstrs misses the queued row")
	}
	h.q.Settle(h.q.Head())
	tk = h.q.Take()
	if !h.q.Commit(tk) {
		t.Fatal("commit refused")
	}
	DropHeld("tq", DropArchived)
	if len(*drops) != 0 {
		t.Fatalf("a committed input was reported as not run: %+v", *drops)
	}
	if HeldInstrs("tq")["i-2"] {
		t.Fatal("a committed row is still held")
	}
}
