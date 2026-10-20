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
