package main

// The Console notification for the background memory review (ADR 0117 decision 6): raised when a
// run leaves proposals in the queue, so the member knows there is something to decide in Settings >
// Agent memory > Changes. It is about the workspace, not a session, and carries counts only: the
// proposals are read on that screen, never from a notification.

import (
	"log"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
)

func notifyMemoryReview(runID string, queued, pending int) error {
	ev := notice.New("memory-review", "", "", "")
	ev.TargetType = "workspace"
	ev.Payload["queued"] = queued
	ev.Payload["pending"] = pending
	// Once per run: a run replayed after a stop calls this again.
	if err := notice.PutOnce("memory-review:"+runID, ev); err != nil {
		// The run keeps its pending mark and the review retries.
		log.Printf("memory-review notification: %v", err)
		return err
	}
	return nil
}
