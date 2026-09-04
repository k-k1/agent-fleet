package main

// noteInput sits on the terminal keystroke relay (proxy.go relay calls onInput() before
// forwarding the key), so any time spent here lands directly on the latency before that
// key echoes back — a synchronous store write on this path stalls one keystroke by a
// whole DB round trip every time the 5s coalescing window lapses. Recording presence
// must never make a keystroke wait.
//
// Breaking that does not break the feature (presence is still recorded, keys still
// arrive); it only shows up as "input catches sometimes", so this test measures the time
// itself.
import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// slowActivityStore reproduces a slow-DB day. The other Store methods are unused, so the
// embedded interface stays nil on purpose: any unexpected call panics and is noticed.
type slowActivityStore struct {
	store.Store
	delay time.Duration
	calls atomic.Int32
}

func (s *slowActivityStore) RecordWorkspaceActivity(context.Context, string, string, string, string) (bool, error) {
	s.calls.Add(1)
	time.Sleep(s.delay)
	return true, nil
}

func TestTerminalNoteInputDoesNotBlockOnTheStore(t *testing.T) {
	const wsID = "ws-latency"
	st := &slowActivityStore{delay: 300 * time.Millisecond}
	m := &manager{conns: newConnRegistry(), store: st}

	release, noteInput, err := m.trackWorkspaceTerminal(context.Background(), wsID, "s1")
	if err != nil {
		t.Fatalf("trackWorkspaceTerminal: %v", err)
	}
	defer release()

	// The forced write at attach time arms the 5s coalescing window. Clear it explicitly
	// so a keystroke actually reaches the write path.
	m.mu.Lock()
	m.activityProtectedUntil[wsID] = time.Time{}
	m.mu.Unlock()
	before := st.calls.Load()

	// A burst of keystrokes. If even one of them waits on the store's response time
	// (300ms), that key reaches the screen 300ms late.
	start := time.Now()
	for i := 0; i < 50; i++ {
		noteInput()
	}
	elapsed := time.Since(start)
	if elapsed > 50*time.Millisecond {
		t.Fatalf("relaying 50 keystrokes took %v (one store round trip = %v) - the keystroke path "+
			"is waiting on the presence write", elapsed, st.delay)
	}

	// Not blocking is not enough: the record must still be written (coalesced, then run
	// asynchronously at least once).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st.calls.Load() > before {
			// The in-memory side of presence must be immediate — the reaper reads it.
			if !m.conns.watched(wsID, time.Minute, time.Now()) {
				t.Fatal("a keystroke happened but it is not counted as presence")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("keystrokes never advanced the shared watermark (making it asynchronous dropped the write itself)")
}
