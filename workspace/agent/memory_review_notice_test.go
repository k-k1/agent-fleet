package main

import (
	"os"
	"path/filepath"

	"encoding/json"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
)

// The notification of a run that left proposals is about the workspace and carries counts only,
// once per run.
func TestMemoryReviewNoticeCarriesCountsOnlyAndIsRaisedOncePerRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	before := len(notice.List())
	notifyMemoryReview("0123456789abcdef0123", 2, 5)
	notifyMemoryReview("0123456789abcdef0123", 2, 5) // a replayed run
	notifyMemoryReview("aaaaaaaaaaaaaaaaaaaa", 1, 6)
	evs := notice.List()
	if len(evs) != before+2 {
		t.Fatalf("events = %d, want %d", len(evs), before+2)
	}
	var mine []notice.Event
	for _, e := range evs {
		if e.Kind == "memory-review" {
			mine = append(mine, e)
		}
	}
	if len(mine) != 2 {
		t.Fatalf("memory-review events = %d", len(mine))
	}
	e := mine[0]
	if e.TargetType != "workspace" || e.SessionName != "" {
		t.Fatalf("event = %+v", e)
	}
	b, _ := json.Marshal(e.Payload)
	if string(b) != `{"pending":5,"queued":2}` && string(b) != `{"pending":6,"queued":1}` {
		t.Fatalf("payload = %s", b)
	}
	if strings.Contains(string(b), "0123456789") {
		t.Fatal("the payload names the run")
	}
}

// An outbox that cannot be written is an error the review sees (it keeps the run's pending mark and
// retries), not a log line.
func TestMemoryReviewNoticeReportsAnOutboxFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// The marker directory is a file: PutOnce cannot create it.
	if err := os.MkdirAll(paths.AgentStateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.AgentStateDir(), "notification-markers"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := notifyMemoryReview("0123456789abcdef0123", 1, 1); err == nil {
		t.Fatal("a failed outbox write was reported as success")
	}
}
