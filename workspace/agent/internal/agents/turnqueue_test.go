package agents

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func member(id string) TurnInput {
	return TurnInput{Prompt: "member " + id, ClientMessageID: id, Origin: Origin{Kind: OriginMember}}
}

func peer(id string) TurnInput {
	return TurnInput{Prompt: "peer " + id, ClientMessageID: id, Origin: Origin{Kind: OriginPeer, From: "other"}}
}

func newQ(t *testing.T, at LedgerPoint) *TurnQueue {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // the ledger writes under HOME
	q := NewTurnQueue("tq", NewMsgLedger("tq-ledger"), at)
	q.now = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) }
	return q
}

// running takes the head and walks it to received, as a pump whose turn has started would.
func running(t *testing.T, q *TurnQueue) *Taken {
	t.Helper()
	h := q.Take()
	if h == nil {
		t.Fatal("nothing to take")
	}
	if !q.Commit(h) {
		t.Fatal("commit refused")
	}
	if q.Received(h) {
		t.Fatal("a stop was pending on a fresh turn")
	}
	return h
}

func ids(items []QueueItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func sameIDs(t *testing.T, what string, got []QueueItem, want ...string) {
	t.Helper()
	if fmt.Sprint(ids(got)) != fmt.Sprint(want) {
		t.Fatalf("%s = %v, want %v", what, ids(got), want)
	}
}

// Decision 1: a first stop ends the running turn and the queue continues, in order.
func TestFirstStopStopsTheTurnAndTheQueueContinues(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("a"))
	a := running(t, q)
	q.Accept(member("b"))
	q.Accept(peer("c"))

	out := q.Interrupt(InterruptOpts{})
	if out.Result.Stop != StopFirst || out.Head != HeadStopNow || out.Result.Discard != nil {
		t.Fatalf("first stop = %+v", out)
	}
	if !q.Episode() {
		t.Fatal("a first stop that leaves input queued opens no episode")
	}
	sameIDs(t, "queue after a first stop", q.Items(), "b", "c")

	q.Settle(a)
	if !q.Episode() {
		t.Fatal("the episode ended while input was still queued")
	}
	b := running(t, q)
	if b.ID() != "b" {
		t.Fatalf("next = %s, want b", b.ID())
	}
	q.Settle(b)
	c := running(t, q)
	q.Settle(c)
	if q.Episode() {
		t.Fatal("the episode outlived the last turn it started")
	}
	if out := q.Interrupt(InterruptOpts{}); out.Result.Stop != StopFirst {
		t.Fatalf("a stop after the episode = %s, want first", out.Result.Stop)
	}
}

// A first stop with nothing queued opens no episode: the next stop is a first stop again.
func TestFirstStopWithNothingQueuedOpensNoEpisode(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("a"))
	running(t, q)
	q.Interrupt(InterruptOpts{})
	if q.Episode() {
		t.Fatal("episode opened with an empty queue")
	}
	if out := q.Interrupt(InterruptOpts{}); out.Result.Stop != StopFirst {
		t.Fatalf("second press with nothing queued = %s, want first", out.Result.Stop)
	}
}

// Decision 2: any stop in the episode stops what runs and discards the rest, peers included,
// and the discard is kept for return.
func TestSecondStopDiscardsTheRestAndKeepsIt(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("a"))
	a := running(t, q)
	q.Accept(member("b"))
	q.Accept(peer("c"))
	q.Accept(member("d"))
	q.Interrupt(InterruptOpts{})
	q.Settle(a)
	b := running(t, q) // the continued turn runs for a while; the member stops it

	out := q.Interrupt(InterruptOpts{})
	if out.Result.Stop != StopSecond || out.Head != HeadStopNow {
		t.Fatalf("second stop = %+v", out)
	}
	d := out.Result.Discard
	if d == nil || d.Reason != DiscardSecondStop || d.At != "2026-09-30T12:00:00Z" {
		t.Fatalf("discard = %+v", d)
	}
	sameIDs(t, "discarded", d.Items, "c", "d")
	if d.Items[0].Origin.Kind != OriginPeer || d.Items[0].State != "" {
		t.Fatalf("discarded peer item = %+v", d.Items[0])
	}
	if q.Episode() || q.Len() != 0 {
		t.Fatalf("after a second stop: episode=%v len=%d", q.Episode(), q.Len())
	}
	sameIDs(t, "kept discards", q.Discards()[0].Items, "c", "d")
	q.Settle(b)
	if out := q.Interrupt(InterruptOpts{}); out.Result.Stop != StopFirst {
		t.Fatalf("the stop after a second stop = %s, want first", out.Result.Stop)
	}
}

// Decision 2: the episode is the driver's own field. New member input ends it; a peer's,
// an operator's or a resend does not.
func TestEpisodeEndsOnNewMemberInputOnly(t *testing.T) {
	for _, at := range []LedgerPoint{LedgerAtAccept, LedgerAtTake} {
		t.Run(fmt.Sprint(at), func(t *testing.T) {
			q := newQ(t, at)
			q.Accept(member("a"))
			running(t, q)
			q.Accept(member("b"))
			q.Interrupt(InterruptOpts{})

			q.Accept(peer("p"))
			q.Accept(TurnInput{Prompt: "x", ClientMessageID: "o", Origin: Origin{Kind: OriginOperator}})
			q.Accept(TurnInput{Prompt: "x", ClientMessageID: "u"}) // unset origin
			if _, dup := q.Accept(member("b")); dup != (at == LedgerAtAccept) {
				t.Fatalf("resend dup = %v", dup)
			}
			if !q.Episode() {
				t.Fatal("non-member input or a resend ended the episode")
			}
			q.Accept(member("new"))
			if q.Episode() {
				t.Fatal("new member input left the episode open")
			}
			for _, k := range []string{OriginDiscord, OriginSlack} {
				q.Interrupt(InterruptOpts{}) // reopen: the queue is not empty
				if !q.Episode() {
					t.Fatal("episode did not reopen")
				}
				q.Accept(TurnInput{Prompt: "x", ClientMessageID: "br-" + k, Origin: Origin{Kind: k}})
				if q.Episode() {
					t.Fatalf("%s input is member input and must end the episode", k)
				}
			}
		})
	}
}

// On LedgerAtTake the ledger is recorded when the pump takes an entry, so an accept-time
// resend check has to look both at the ledger and at the queue — without recording.
func TestLedgerAtTakeResendCheckRecordsNothing(t *testing.T) {
	q := newQ(t, LedgerAtTake)
	q.Accept(member("a"))
	if q.ledger.Seen("tq", "a") {
		t.Fatal("accept recorded the ledger on LedgerAtTake")
	}
	a := running(t, q)
	if !q.ledger.Seen("tq", "a") {
		t.Fatal("take did not record the ledger")
	}
	q.Settle(a)
	q.Accept(member("a")) // a resend of something already started
	if h := q.Take(); h != nil {
		t.Fatalf("a resend was taken: %s", h.ID())
	}
}

func TestLedgerAtAcceptDropsResend(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("a"))
	if _, dup := q.Accept(member("a")); !dup || q.Len() != 1 {
		t.Fatalf("dup=%v len=%d", dup, q.Len())
	}
}

// Decision 3: the brake discards everything unsent whatever the episode state.
func TestDiscardQueueWorksOutsideAnEpisode(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("a"))
	running(t, q)
	q.Accept(member("b"))
	out := q.Interrupt(InterruptOpts{DiscardQueue: true})
	if out.Result.Stop != StopDiscard || out.Head != HeadStopNow || out.Result.Discard == nil ||
		out.Result.Discard.Reason != DiscardQueue {
		t.Fatalf("discard = %+v", out)
	}
	sameIDs(t, "discarded", out.Result.Discard.Items, "b")

	// Nothing queued: still a stop, and no empty discard is kept.
	q2 := newQ(t, LedgerAtAccept)
	if out := q2.Interrupt(InterruptOpts{DiscardQueue: true}); out.Result.Discard != nil || q2.Discards() != nil {
		t.Fatalf("empty discard kept: %+v", out)
	}
}

// Decision 3: taken is not sent. A discard that takes the lock before the commit cancels the
// entry; one that comes after it leaves the stop to the hand-over, delivered exactly once.
func TestTakenEntryCancellableUntilCommit(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("a"))
	h := q.Take()
	out := q.Interrupt(InterruptOpts{DiscardQueue: true})
	if out.Head != HeadCancelled {
		t.Fatalf("head = %v, want cancelled", out.Head)
	}
	sameIDs(t, "discarded", out.Result.Discard.Items, "a")
	if q.Commit(h) {
		t.Fatal("a cancelled entry committed: it would start after the brake")
	}
	if q.Head() != nil {
		t.Fatal("the cancelled head is still taken")
	}
}

func TestCommittedEntryStopHandOver(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("a"))
	q.Accept(member("b"))
	h := q.Take()
	q.Commit(h)
	out := q.Interrupt(InterruptOpts{DiscardQueue: true})
	if out.Head != HeadStopPending {
		t.Fatalf("head = %v, want stop-pending", out.Head)
	}
	sameIDs(t, "discarded (committed is not)", out.Result.Discard.Items, "b")
	if !q.Received(h) {
		t.Fatal("the pump was not told to deliver the pending stop")
	}
	if q.Received(h) {
		t.Fatal("the pending stop was handed over twice")
	}
	// Received first, stop after: the stopping side delivers, at once.
	q2 := newQ(t, LedgerAtAccept)
	q2.Accept(member("a"))
	running(t, q2)
	if out := q2.Interrupt(InterruptOpts{}); out.Head != HeadStopNow {
		t.Fatalf("head = %v, want stop-now", out.Head)
	}
}

// A first stop on the lone input whose start is in flight stops it (it is the turn being
// stopped), and it is not kept as a discard.
func TestFirstStopOnLoneStartingInput(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("a"))
	h := q.Take()
	out := q.Interrupt(InterruptOpts{})
	if out.Head != HeadCancelled || out.Result.Discard != nil || q.Episode() {
		t.Fatalf("out = %+v episode=%v", out, q.Episode())
	}
	if q.Commit(h) {
		t.Fatal("the stopped start still committed")
	}
	q3 := newQ(t, LedgerAtAccept)
	q3.Accept(member("a"))
	h3 := q3.Take()
	q3.Commit(h3)
	if out := q3.Interrupt(InterruptOpts{}); out.Head != HeadStopPending {
		t.Fatalf("committed lone start: head = %v, want stop-pending", out.Head)
	}
	if !q3.Received(h3) {
		t.Fatal("stop not handed over")
	}
}

// Decision 1: input the pump holds behind another client's turn (opencode) or that the runtime
// holds behind a turn this driver did not start (muse) is queued: a first stop lets it
// continue, a second stop cancels or stops it.
func TestHeldEntryIsQueued(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("a"))
	h := q.Take()
	q.Hold(h, true)
	out := q.Interrupt(InterruptOpts{})
	if out.Head != HeadKept || !q.Episode() {
		t.Fatalf("first stop on a held entry: %+v episode=%v", out, q.Episode())
	}
	sameIDs(t, "shown", q.Items(), "a")
	out = q.Interrupt(InterruptOpts{})
	if out.Result.Stop != StopSecond || out.Head != HeadCancelled {
		t.Fatalf("second stop on a held entry: %+v", out)
	}
	sameIDs(t, "discarded", out.Result.Discard.Items, "a")

	// Sent to the runtime behind another turn: shown as sent, stopped when it starts.
	q2 := newQ(t, LedgerAtAccept)
	q2.Accept(member("a"))
	h2 := q2.Take()
	q2.Commit(h2)
	q2.Hold(h2, true)
	if it := q2.Items(); len(it) != 1 || it[0].State != EntrySent {
		t.Fatalf("items = %+v", it)
	}
	if out := q2.Interrupt(InterruptOpts{}); out.Head != HeadKept {
		t.Fatalf("first stop: %v", out.Head)
	}
	if out := q2.Interrupt(InterruptOpts{}); out.Head != HeadStopPending || out.Result.Discard != nil {
		t.Fatalf("second stop: %+v", out)
	}
	if !q2.Received(h2) {
		t.Fatal("stop not handed over when the held turn started")
	}
}

// Decision 5: removal by id draws the same line as a discard.
func TestRemove(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("a"))
	q.Accept(member("b"))
	q.Accept(TurnInput{Prompt: "c", ClientMessageID: "c", Attachments: []string{"/x.png"}, Origin: Origin{Kind: OriginMember}})
	h := q.Take()

	it, err := q.Remove("c")
	if err != nil || it.Text != "c" || len(it.Attachments) != 1 || it.State != "" {
		t.Fatalf("remove queued = %+v, %v", it, err)
	}
	if _, err := q.Remove("a"); err != nil {
		t.Fatalf("remove taken-but-uncommitted: %v", err)
	}
	if q.Commit(h) {
		t.Fatal("a removed entry committed")
	}
	h = q.Take()
	q.Commit(h)
	if _, err := q.Remove("b"); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("remove committed = %v, want already started", err)
	}
	if _, err := q.Remove("zzz"); !errors.Is(err, ErrNotQueued) {
		t.Fatalf("remove unknown = %v, want not queued", err)
	}
}

// Removing the last queued entry ends the episode once nothing is taken.
func TestRemoveEndsTheEpisodeWhenNothingIsLeft(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("a"))
	a := running(t, q)
	q.Accept(peer("b"))
	q.Interrupt(InterruptOpts{})
	q.Settle(a)
	if _, err := q.Remove("b"); err != nil {
		t.Fatal(err)
	}
	if q.Episode() {
		t.Fatal("episode open with nothing queued or running")
	}
}

// Decision 4: discards are kept per discard, the last five, and a dismissal drops one.
func TestDiscardsKeptPerDiscardCappedAndDismissed(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	var first string
	for i := 0; i < maxDiscards+1; i++ {
		q.Accept(member(fmt.Sprint("m", i)))
		d := q.Interrupt(InterruptOpts{DiscardQueue: true}).Result.Discard
		if i == 0 {
			first = d.ID
		}
	}
	ds := q.Discards()
	if len(ds) != maxDiscards || ds[0].Items[0].ID != "m1" || ds[len(ds)-1].Items[0].ID != "m5" {
		t.Fatalf("kept = %+v", ds)
	}
	if q.DismissDiscard(first) {
		t.Fatal("the oldest discard should have been pushed out")
	}
	if !q.DismissDiscard(ds[2].ID) || q.DismissDiscard(ds[2].ID) || len(q.Discards()) != maxDiscards-1 {
		t.Fatal("dismiss did not drop exactly that discard")
	}
	if len(q.Discards()) > 0 && q.Discards()[0].ID != ds[0].ID {
		t.Fatal("dismiss disturbed the order")
	}
}

// Requeue returns an entry the runtime never received — unless a stop was aimed at it.
func TestRequeue(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("a"))
	q.Accept(member("b"))
	h := q.Take()
	q.Commit(h)
	if !q.Requeue(h) {
		t.Fatal("requeue refused")
	}
	sameIDs(t, "after requeue", q.Items(), "a", "b")

	h = q.Take()
	q.Commit(h)
	q.Interrupt(InterruptOpts{}) // first stop: the committed start is the turn being stopped
	if q.Requeue(h) {
		t.Fatal("an entry with a stop pending was put back and would start later")
	}
	sameIDs(t, "after a stopped requeue", q.Items(), "b")
}

// Decision 8: teardown discards everything and keeps nothing for return.
func TestDropAllKeepsNothing(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("a"))
	running(t, q)
	q.Accept(member("b"))
	q.Interrupt(InterruptOpts{})
	q.DropAll()
	if q.Len() != 0 || q.Episode() || q.Discards() != nil {
		t.Fatalf("after DropAll: len=%d episode=%v discards=%v", q.Len(), q.Episode(), q.Discards())
	}
}

func TestItemsStates(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("a"))
	q.Accept(member("b"))
	h := q.Take()
	if it := q.Items(); it[0].State != EntryQueued || it[1].State != EntryQueued {
		t.Fatalf("taken = %+v", it)
	}
	q.Commit(h)
	if it := q.Items(); it[0].State != EntryCommitted {
		t.Fatalf("committed = %+v", it)
	}
	q.Received(h)
	sameIDs(t, "received is a turn, not queued", q.Items(), "b")
	if fmt.Sprint(q.Texts()) != "[member b]" {
		t.Fatalf("texts = %v", q.Texts())
	}
}

// A stale token (an earlier generation's pump) cannot commit or settle the entry that replaced it.
func TestStaleTokenIsIgnored(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("a"))
	q.Accept(member("b"))
	a := q.Take()
	q.Remove("a")
	b := q.Take()
	if q.Commit(a) {
		t.Fatal("stale commit")
	}
	q.Settle(a)
	if q.Head() != b {
		t.Fatal("stale settle released the live entry")
	}
}
