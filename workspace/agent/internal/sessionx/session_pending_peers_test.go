package sessionx

import (
	"encoding/json"
	"strings"
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
	tgt.mu.Lock()
	if !agents.TakePendingPeer("kid", "a") {
		tgt.mu.Unlock()
		t.Fatal("claim")
	}
	tgt.inflight = &agents.PendingPeer{ID: "a", From: "parent", QueuedAt: t0}
	tgt.mu.Unlock()
	t.Cleanup(func() { tgt.mu.Lock(); tgt.inflight = nil; tgt.mu.Unlock() })
	if err := agents.PutPendingPeer("kid", agents.PendingPeer{ID: "c", From: "parent", Message: "y", QueuedAt: t0.Add(3 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(wireSession(m, false))
	if err != nil || !strings.Contains(string(b), `"pendingPeers":{"count":2,"newestAt":"2026-11-26T10:03:00Z"}`) {
		t.Fatalf("wire = %s %v", b, err)
	}

	tgt.mu.Lock()
	tgt.inflight = nil
	tgt.mu.Unlock()
	agents.TakePendingPeer("kid", "c")
	if s := wireSession(m, false); s.PendingPeers != nil {
		t.Fatalf("delivered, row still reports %+v", s.PendingPeers)
	}
}
