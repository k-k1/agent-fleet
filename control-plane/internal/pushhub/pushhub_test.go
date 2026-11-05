package pushhub

import (
	"context"
	"testing"
	"time"
)

// leave only cancels the ticker; Wait must not return until a build already in flight has
// returned, because that build may still be using what the caller is about to tear down.
func TestWaitCoversBuildInFlightAfterLeave(t *testing.T) {
	var h Hub[int, int]
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	build := func(ctx context.Context, _ int) int {
		close(entered)
		<-release
		close(finished)
		return 1
	}
	_, leave := h.Join("k", 0, time.Hour, build)
	<-entered
	leave()
	if h.Active() != 0 {
		t.Fatal("key still active after the last leave")
	}

	waited := make(chan struct{})
	go func() { h.Wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("Wait returned while a build was still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-waited
	select {
	case <-finished:
	default:
		t.Fatal("Wait returned before the build finished")
	}
}

func TestWaitWithoutHubsReturns(t *testing.T) {
	var h Hub[int, int]
	h.Wait()
}
