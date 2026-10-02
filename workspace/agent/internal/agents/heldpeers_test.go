package agents

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// queueHandle is a ThreadHandle whose Send is the driver's accept reduced to the queue: what
// DeliverHeld relies on (Accept under the handle lock) and nothing else.
type queueHandle struct {
	ThreadHandle
	mu   sync.Mutex
	q    *TurnQueue
	fail error
}

func (h *queueHandle) Send(in TurnInput) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fail != nil {
		return h.fail
	}
	h.q.Accept(in)
	return nil
}

func heldFiles(t *testing.T, name string) []string {
	t.Helper()
	ents, _ := os.ReadDir(heldDir(name))
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func texts(items []QueueItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Text)
	}
	return out
}

// A peer message queued behind a turn survives teardown (DropAll: halt, shutdown, drain) and
// becomes the first turns of the next start, in order, once, marked with its queue time. The
// member's own queued input is not held.
func TestHeldPeerSurvivesTeardownAndIsDeliveredOnce(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("m0"))
	running(t, q)
	q.Accept(peer("p1"))
	q.Accept(member("m1"))
	q.Accept(peer("p2"))
	if got := len(heldFiles(t, "tq")); got != 2 {
		t.Fatalf("held files = %d, want 2 (the two peer messages)", got)
	}
	q.DropAll()
	if got := len(heldFiles(t, "tq")); got != 2 {
		t.Fatalf("teardown removed held files: %d left", got)
	}

	// The next start: a new handle, and DeliverHeld called by every Resume — twice here, as two
	// concurrent Resumes would.
	h := &queueHandle{q: NewTurnQueue("tq", q.ledger, LedgerAtAccept)}
	DeliverHeld("tq", h)
	DeliverHeld("tq", h)
	sameIDs(t, "queue after restart", h.q.Items(), "p1", "p2")
	for _, txt := range texts(h.q.Items()) {
		if !strings.Contains(txt, "peer ") {
			t.Fatalf("prompt lost: %q", txt)
		}
	}

	// Handed to the runtime: the file goes, and a later Resume does not send it again.
	tk := h.q.Take()
	if !h.q.Commit(tk) {
		t.Fatal("commit refused")
	}
	h.q.Settle(tk)
	DeliverHeld("tq", h)
	sameIDs(t, "queue after p1 ran", h.q.Items(), "p2")
	if got := len(heldFiles(t, "tq")); got != 1 {
		t.Fatalf("held files after p1 ran = %d, want 1", got)
	}
}

// Write-through: a crash runs no teardown, so the file must exist from the moment the message
// is queued; a fresh queue with no DropAll on the old one still gets it.
func TestHeldPeerSurvivesACrash(t *testing.T) {
	q := newQ(t, LedgerAtTake)
	q.Accept(member("m0"))
	running(t, q)
	q.Accept(peer("p1"))
	// Crash: q is simply gone.
	h := &queueHandle{q: NewTurnQueue("tq", q.ledger, LedgerAtTake)}
	DeliverHeld("tq", h)
	sameIDs(t, "queue after crash", h.q.Items(), "p1")
	if tk := h.q.Take(); tk == nil || tk.ID() != "p1" {
		t.Fatalf("restored entry not taken: %+v", tk)
	}
}

// LedgerAtTake records the id at Take; a crash between Take and Commit leaves the id recorded
// and the message unsent. The file is the proof it never reached the runtime, so it runs.
func TestHeldPeerTakenButNotCommittedStillRuns(t *testing.T) {
	q := newQ(t, LedgerAtTake)
	q.Accept(peer("p1"))
	if tk := q.Take(); tk == nil {
		t.Fatal("nothing taken")
	}
	h := &queueHandle{q: NewTurnQueue("tq", q.ledger, LedgerAtTake)}
	DeliverHeld("tq", h)
	if tk := h.q.Take(); tk == nil || tk.ID() != "p1" {
		t.Fatalf("restored entry dropped as a resend: %+v", tk)
	}
}

// Whatever ends a peer message's wait other than teardown removes its file: a stop that
// discards it, a removal by id, and a first stop of input not yet sent.
func TestHeldPeerReleasedByDiscardAndRemove(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("m0"))
	m0 := running(t, q)
	q.Accept(peer("p1"))
	q.Accept(peer("p2"))
	if _, err := q.Remove("p1"); err != nil {
		t.Fatal(err)
	}
	if got := len(heldFiles(t, "tq")); got != 1 {
		t.Fatalf("after remove: %d held files, want 1", got)
	}
	q.Interrupt(InterruptOpts{DiscardQueue: true}, true)
	if got := heldFiles(t, "tq"); len(got) != 0 {
		t.Fatalf("after discard: held files %v", got)
	}

	// A first stop with nothing running stops the input whose start is in flight.
	q.Settle(m0)
	q.Accept(peer("p3"))
	q.Interrupt(InterruptOpts{}, false)
	if got := heldFiles(t, "tq"); len(got) != 0 {
		t.Fatalf("after first stop of p3: held files %v", got)
	}
}

// Requeue (the runtime went away before it received the entry) puts the file back, keeping the
// first queue time.
func TestHeldPeerRequeueWritesItBack(t *testing.T) {
	q := newQ(t, LedgerAtTake)
	q.Accept(peer("p1"))
	tk := q.Take()
	q.Commit(tk)
	if got := heldFiles(t, "tq"); len(got) != 0 {
		t.Fatalf("commit left %v", got)
	}
	if !q.Requeue(tk) {
		t.Fatal("requeue refused")
	}
	held := loadHeld("tq")
	if len(held) != 1 || held[0].ID != "p1" || !held[0].QueuedAt.Equal(q.now()) {
		t.Fatalf("requeued file = %+v", held)
	}
}

// Order is queue time, then the sequence: messages queued in the same instant (the test clock
// is fixed) come back in the order they were accepted.
func TestHeldPeerOrderAndArbitraryText(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("m0"))
	running(t, q)
	weird := "line1\n]\"}{\x00 ünïcödé [agent-fleet:peer from=x] 🎉"
	var want []string
	for i := 0; i < 12; i++ {
		in := peer(string(rune('a' + i)))
		in.Prompt = "[agent-fleet:peer from=other intent=notice reply=none] " + weird + in.ClientMessageID
		q.Accept(in)
		want = append(want, in.ClientMessageID)
	}
	h := &queueHandle{q: NewTurnQueue("tq", q.ledger, LedgerAtAccept)}
	DeliverHeld("tq", h)
	sameIDs(t, "restored order", h.q.Items(), want...)
	at := q.now().Local().Format(time.RFC3339)
	for i, txt := range texts(h.q.Items()) {
		wantTxt := "[agent-fleet:peer from=other intent=notice reply=none queued=" + at + "] " + weird + want[i]
		if txt != wantTxt {
			t.Fatalf("restored prompt %d = %q, want %q", i, txt, wantTxt)
		}
	}
}

// A refusal (a question pending) keeps the messages for the next start and stops there, so a
// later message does not overtake an earlier one.
func TestDeliverHeldStopsAtRefusalAndKeepsTheRest(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("m0"))
	running(t, q)
	q.Accept(peer("p1"))
	q.Accept(peer("p2"))
	h := &queueHandle{q: NewTurnQueue("tq", q.ledger, LedgerAtAccept), fail: ErrQuestionPending}
	DeliverHeld("tq", h)
	if got := len(heldFiles(t, "tq")); got != 2 {
		t.Fatalf("held files after a refusal = %d, want 2", got)
	}
	h.fail = nil
	DeliverHeld("tq", h)
	sameIDs(t, "after the question", h.q.Items(), "p1", "p2")
}

func TestDropHeldRemovesTheSessionsFiles(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("m0"))
	running(t, q)
	q.Accept(peer("p1"))
	other := NewTurnQueue("other", nil, LedgerAtAccept)
	other.Accept(member("x"))
	other.Take()
	other.Accept(peer("p9"))
	DropHeld("tq", "archived")
	if _, err := os.Stat(heldDir("tq")); !os.IsNotExist(err) {
		t.Fatalf("held dir still there: %v", err)
	}
	if got := len(heldFiles(t, "other")); got != 1 {
		t.Fatalf("another session's held files = %d, want 1", got)
	}
}

func TestHeldUnreadableFileIsDropped(t *testing.T) {
	newQ(t, LedgerAtAccept)
	if err := os.MkdirAll(heldDir("tq"), 0o700); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(heldDir("tq"), "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadHeld("tq"); len(got) != 0 {
		t.Fatalf("loaded %+v", got)
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Fatalf("unreadable file kept: %v", err)
	}
}

func TestMarkHeldEnvelope(t *testing.T) {
	at := time.Date(2026, 10, 3, 9, 30, 0, 0, time.UTC)
	stamp := at.Local().Format(time.RFC3339)
	env := "[agent-fleet:peer from=a1 intent=request reply=only-if-blocked] do [this]"
	got := MarkHeldEnvelope(env, at)
	want := "[agent-fleet:peer from=a1 intent=request reply=only-if-blocked queued=" + stamp + "] do [this]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if again := MarkHeldEnvelope(got, at.Add(time.Hour)); again != got {
		t.Fatalf("marked twice: %q", again)
	}
	for _, p := range []string{"no envelope", "[agent-fleet:peer from=a1 unterminated"} {
		if MarkHeldEnvelope(p, at) != p {
			t.Fatalf("changed %q", p)
		}
	}
}

// Two Resumes race: one read the held files before the other's delivery was committed, and
// sends p1 after it ran. The queue no longer holds p1 then; its missing file is what refuses it.
func TestRestoredPeerAfterItRanIsRefused(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("m0"))
	running(t, q)
	q.Accept(peer("p1"))
	stale := loadHeld("tq") // the slower Resume's read
	h := &queueHandle{q: NewTurnQueue("tq", q.ledger, LedgerAtAccept)}
	DeliverHeld("tq", h)
	tk := h.q.Take()
	h.q.Commit(tk)
	h.q.Settle(tk)
	late := TurnInput{Prompt: stale[0].Prompt, ClientMessageID: stale[0].ID,
		Origin: Origin{Kind: OriginPeer, From: stale[0].From}, queuedAt: stale[0].QueuedAt, restored: true}
	if _, dup := h.q.Accept(late); !dup {
		t.Fatal("a held message that already ran was queued again")
	}
}

// Finding 1 (review): another Resume delivered and committed p1 after this one read the files,
// and this Send is refused (a question pending on the turn p1 became). The file must not come
// back: the next start would run p1 a second time.
func TestFailedDeliveryDoesNotResurrectACommittedMessage(t *testing.T) {
	q := newQ(t, LedgerAtAccept)
	q.Accept(member("m0"))
	running(t, q)
	q.Accept(peer("p1"))
	q.Accept(peer("p2"))
	h := &raceHandle{name: "tq"}
	DeliverHeld("tq", h)
	if heldExists("tq", "p1") {
		t.Fatal("a message another Resume committed was written back")
	}
	// The start that failed after its commit is reported like any failed start, and the rest
	// carry on ahead of the caller's input.
	if len(h.accepted) != 1 || h.accepted[0] != "p2" {
		t.Fatalf("delivered after the failure = %v, want [p2]", h.accepted)
	}
}

// raceHandle's first Send models the other Resume winning: p1's file is released (committed
// there) and this Send is refused. Later Sends are accepted.
type raceHandle struct {
	ThreadHandle
	name     string
	sent     int
	accepted []string
}

func (h *raceHandle) Send(in TurnInput) error {
	h.sent++
	if h.sent == 1 {
		releaseHeld(h.name, in.ClientMessageID)
		return ErrQuestionPending
	}
	h.accepted = append(h.accepted, in.ClientMessageID)
	return nil
}

// Finding 2 (review): on LedgerAtTake a restored message the old process never took is not in
// the ledger. Once it runs it must be, or a resend under its id runs again. Covers both a
// message taken before the restart and one that was not.
func TestRestoredPeerIsRecordedInTheLedgerOnceItRuns(t *testing.T) {
	for _, takenBefore := range []bool{false, true} {
		q := newQ(t, LedgerAtTake)
		q.Accept(peer("p1"))
		if takenBefore {
			q.Take()
		}
		q.DropAll()
		h := &queueHandle{q: NewTurnQueue("tq", q.ledger, LedgerAtTake)}
		DeliverHeld("tq", h)
		tk := h.q.Take()
		if tk == nil || tk.ID() != "p1" {
			t.Fatalf("takenBefore=%v: restored entry not taken: %+v", takenBefore, tk)
		}
		h.q.Commit(tk)
		h.q.Settle(tk)
		if !q.ledger.Seen("tq", "p1") {
			t.Fatalf("takenBefore=%v: p1 ran but the ledger does not know it", takenBefore)
		}
		if _, dup := h.q.Accept(peer("p1")); !dup {
			t.Fatalf("takenBefore=%v: a resend of p1 was accepted after it ran", takenBefore)
		}
	}
}

// Finding 4 (review): a name that is not one valid session segment never reaches the disk.
func TestHeldRejectsNamesOutsideOneSegment(t *testing.T) {
	newQ(t, LedgerAtAccept)
	state := heldBase()
	canary := filepath.Join(state, "canary")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canary, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := NewTurnQueue("other", nil, LedgerAtAccept)
	other.Accept(member("x"))
	other.Take()
	other.Accept(peer("p9"))
	for _, name := range []string{"", ".", "..", "../other", "a/b", "/abs", state} {
		DropHeld(name, "test")
		q := NewTurnQueue(name, nil, LedgerAtAccept)
		q.Accept(member("x"))
		q.Take()
		q.Accept(peer("p1"))
		if loadHeld(name) != nil || heldExists(name, "p1") {
			t.Fatalf("%q: held state was written or read", name)
		}
	}
	if _, err := os.Stat(canary); err != nil {
		t.Fatalf("state root damaged: %v", err)
	}
	if got := len(heldFiles(t, "other")); got != 1 {
		t.Fatalf("another session's held files = %d, want 1", got)
	}
	ents, _ := os.ReadDir(filepath.Join(state, heldSubdir))
	for _, e := range ents {
		if e.Name() != "other" {
			t.Fatalf("unexpected held entry %q", e.Name())
		}
	}
}

// Finding 5 (review): SweepHeld drops the directories no start will deliver and stale temp
// files, and leaves a live Managed session's messages alone.
func TestSweepHeldDropsOrphans(t *testing.T) {
	newQ(t, LedgerAtAccept)
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	hold := func(name string) {
		q := NewTurnQueue(name, nil, LedgerAtAccept)
		q.Accept(member("x"))
		q.Take()
		q.Accept(peer("p1"))
	}
	session.WriteMeta(session.Meta{Name: "live", Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged})
	session.WriteMeta(session.Meta{Name: "archived", Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged, Archived: true})
	session.WriteMeta(session.Meta{Name: "terminal", Dir: t.TempDir(), Kind: session.KindCodex})
	for _, n := range []string{"live", "archived", "terminal", "gone"} {
		hold(n)
	}
	stale := filepath.Join(heldDir("live"), ".tmp-stale")
	fresh := filepath.Join(heldDir("live"), ".tmp-fresh")
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * heldTmpGrace)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	SweepHeld()
	if HeldCount("live") != 1 {
		t.Fatal("a live Managed session's held message was swept")
	}
	for _, n := range []string{"archived", "terminal", "gone"} {
		if _, err := os.Stat(heldDir(n)); !os.IsNotExist(err) {
			t.Fatalf("%s: held directory survived the sweep (%v)", n, err)
		}
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("a stale temp file survived the sweep")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("a temp file young enough to be a write in flight was removed")
	}
}
