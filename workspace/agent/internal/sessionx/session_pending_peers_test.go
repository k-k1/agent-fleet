package sessionx

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// #5270: the peer messages still on their way to a session reach its row, the one claimed from
// the spool and in /input included, so a queue run is not closed under a message in transit.
func TestRowReportsPendingPeerMessages(t *testing.T) {
	pendingTestEnv(t)
	writeMeta(t, session.Meta{Name: "kid"})
	m, _ := session.ReadMeta("kid")
	if s := wireSession(m, false); s.PendingPeers != nil {
		t.Fatalf("nothing queued, row reports %+v", s.PendingPeers)
	}

	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	if err := agents.PutPendingPeer("kid", agents.PendingPeer{ID: "a", From: "parent", Message: "x", QueuedAt: t0}); err != nil {
		t.Fatal(err)
	}
	// A message the kid sent is not on its way to the kid.
	if err := agents.PutPendingPeer("parent", agents.PendingPeer{ID: "b", From: "kid", Message: "x", QueuedAt: t0.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	for _, alive := range []bool{true, false} {
		s := wireSession(m, alive)
		if s.PendingPeers == nil || s.PendingPeers.Count != 1 || s.PendingPeers.NewestAt != "2026-11-26T10:00:00Z" {
			t.Fatalf("alive=%v: one pending message, row reports %+v", alive, s.PendingPeers)
		}
	}

	// Claimed for delivery: out of the spool, still not delivered.
	tgt := pendingTargetOf("kid")
	setInflight := func(p *agents.PendingPeer) {
		tgt.mu.Lock()
		tgt.view.Lock()
		tgt.inflight = p
		tgt.view.Unlock()
		tgt.mu.Unlock()
	}
	if !agents.TakePendingPeer("kid", "a") {
		t.Fatal("claim")
	}
	setInflight(&agents.PendingPeer{ID: "a", From: "parent", QueuedAt: t0})
	t.Cleanup(func() { setInflight(nil) })
	if err := agents.PutPendingPeer("kid", agents.PendingPeer{ID: "c", From: "parent", Message: "y", QueuedAt: t0.Add(3 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(wireSession(m, false))
	if err != nil || !strings.Contains(string(b), `"pendingPeers":{"count":2,"newestAt":"2026-11-26T10:03:00Z"}`) {
		t.Fatalf("wire = %s %v", b, err)
	}

	setInflight(nil)
	agents.TakePendingPeer("kid", "c")
	if s := wireSession(m, false); s.PendingPeers != nil {
		t.Fatalf("delivered, row still reports %+v", s.PendingPeers)
	}
}

// The list does not wait for a peer send's queue decision, which holds the target's lock across
// pane checks that run tmux.
func TestRowDoesNotWaitForAPeerSendsPaneCheck(t *testing.T) {
	pendingTestEnv(t)
	writeMeta(t, session.Meta{Name: "kid"})
	m, _ := session.ReadMeta("kid")
	entered, release := make(chan struct{}), make(chan struct{})
	prev := peerQueueBlocker
	peerQueueBlocker = func(session.Meta) string {
		close(entered)
		<-release
		return ""
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		peerQueueBlocker = prev
	})
	go func() {
		defer close(done)
		_, _, _ = enqueueDecision("kid", m, "parent", "request", "x", false, func() error { return nil })
	}()
	<-entered
	listed := make(chan session.Session, 1)
	go func() { listed <- wireSession(m, false) }()
	select {
	case <-listed:
	case <-time.After(5 * time.Second):
		close(release)
		<-done
		t.Fatal("the row waited for a peer send's pane check")
	}
	close(release)
	<-done
}

// A delivery refused with 409 goes back in the spool: the row reports the same one message, with
// its queue time, before, during and after, never two (spool and in flight) or none.
func TestRowCountsARefusedDeliveryOnce(t *testing.T) {
	pendingTestEnv(t)
	h := &blockingFakeHandle{inter: &agents.Interaction{ID: "q", Kind: agents.InteractionQuestion}}
	installManagedFake(t, h)
	const name, from = "pp_rowback", "pp_sender"
	queued := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	want := session.PendingPeers{Count: 1, NewestAt: "2026-11-26T10:00:00Z"}
	var mu sync.Mutex
	var seen []session.PendingPeers
	prevReady, prevBack := peerDeliveryReady, pendingWrittenBack
	peerDeliveryReady = func(session.Meta) (bool, bool) { return true, true } // misses the question
	pendingWrittenBack = func(n string) {
		var s session.Session
		fillPendingPeers(&s, n)
		mu.Lock()
		defer mu.Unlock()
		if s.PendingPeers == nil {
			seen = append(seen, session.PendingPeers{})
		} else {
			seen = append(seen, *s.PendingPeers)
		}
	}
	t.Cleanup(func() {
		drainPendingLoops()
		peerDeliveryReady, pendingWrittenBack = prevReady, prevBack
	})
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged})
	session.WriteMeta(session.Meta{Name: from, Dir: t.TempDir(), Kind: session.KindClaude})
	if err := agents.PutPendingPeer(name, agents.PendingPeer{ID: "af_x", From: from, Intent: "notice",
		Message: "still here", QueuedAt: queued}); err != nil {
		t.Fatal(err)
	}
	kickPendingPeers(name)
	waitFor(t, "two refused deliveries", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) >= 2
	})
	h.set(nil)
	waitFor(t, "delivery", func() bool { return len(h.sent()) == 1 })
	mu.Lock()
	defer mu.Unlock()
	for i, got := range seen {
		if got != want {
			t.Fatalf("write-back %d: row reports %+v, want %+v", i, got, want)
		}
	}
}
