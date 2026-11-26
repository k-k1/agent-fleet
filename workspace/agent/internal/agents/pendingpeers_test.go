package agents

import (
	"testing"
	"time"
)

func TestQueuedPeerTouches(t *testing.T) {
	dir := t.TempDir()
	old := heldBase
	heldBase = func() string { return dir }
	t.Cleanup(func() { heldBase = old })

	members := map[string]bool{"parent": true, "kid": true}
	if QueuedPeerTouches(members) {
		t.Fatal("empty spools must not touch the tree")
	}
	if err := PutPendingPeer("outsider", PendingPeer{ID: "a", From: "stranger", Message: "x", QueuedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if QueuedPeerTouches(members) {
		t.Fatal("a message between two outsiders must not touch the tree")
	}
	if err := PutPendingPeer("outsider", PendingPeer{ID: "b", From: "kid", Message: "x", QueuedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if !QueuedPeerTouches(members) {
		t.Fatal("a pending message FROM a tree member must touch the tree")
	}
	TakePendingPeer("outsider", "b")
	if err := PutPendingPeer("parent", PendingPeer{ID: "c", From: "stranger", Message: "x", QueuedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if !QueuedPeerTouches(members) {
		t.Fatal("a pending message TO a tree member must touch the tree")
	}
}

func TestQueuedPeersTo(t *testing.T) {
	dir := t.TempDir()
	old := heldBase
	heldBase = func() string { return dir }
	t.Cleanup(func() { heldBase = old })

	if n, newest := QueuedPeersTo("kid"); n != 0 || !newest.IsZero() {
		t.Fatalf("empty spools: got %d, %v", n, newest)
	}
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	if err := PutPendingPeer("kid", PendingPeer{ID: "a", From: "parent", Message: "x", QueuedAt: t0}); err != nil {
		t.Fatal(err)
	}
	// A message the kid sent elsewhere is not addressed to it.
	if err := PutPendingPeer("other", PendingPeer{ID: "b", From: "kid", Message: "x", QueuedAt: t0.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	// Held peer input counts with the pending one; held input of another origin does not.
	if !putHeld("kid", TurnInput{ClientMessageID: "c", Prompt: "x", Origin: Origin{Kind: OriginPeer, From: "parent"}, queuedAt: t0.Add(2 * time.Minute)}) {
		t.Fatal("put held peer")
	}
	if !putHeld("kid", TurnInput{ClientMessageID: "d", Prompt: "x", Origin: Origin{Kind: OriginOperator}, queuedAt: t0.Add(5 * time.Minute)}) {
		t.Fatal("put held operator")
	}
	n, newest := QueuedPeersTo("kid")
	if n != 2 || !newest.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("got %d, %v; want 2 peer messages, newest %v", n, newest, t0.Add(2*time.Minute))
	}
}
