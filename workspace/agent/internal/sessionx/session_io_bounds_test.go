package sessionx

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// holdSecretsStore keeps the credential store locked (in-process mutex and flock) until the
// returned release is called, which is what a stuck writer looks like to a mirror.
func holdSecretsStore(t *testing.T) (release func()) {
	t.Helper()
	locked, unlock, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_ = secrets.Update(func(*secrets.Data) error {
			close(locked)
			<-unlock
			return nil
		})
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("could not take the secrets store")
	}
	var once bool
	release = func() {
		if !once {
			once = true
			close(unlock)
			<-done
		}
	}
	t.Cleanup(release)
	return release
}

// A mirror blocked on the store must give up at its deadline instead of living until the
// holder lets go (#3421).
func TestInputMirrorLockWaitIsBounded(t *testing.T) {
	withTempHome(t)
	prev := inputMirrorLockWait
	inputMirrorLockWait = 100 * time.Millisecond
	t.Cleanup(func() { inputMirrorLockWait = prev })
	holdSecretsStore(t)

	mirrorUserInputAsync("bounded", "hello")
	finished := make(chan struct{})
	go func() { WaitInputMirrors(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("mirror still waiting on the store long after its deadline")
	}
}

// Mirrors beyond the cap are dropped rather than queued, and the slots come back once the
// running ones finish.
func TestInputMirrorsAreCapped(t *testing.T) {
	withTempHome(t)
	prevMax, prevWait := maxInputMirrors, inputMirrorLockWait
	maxInputMirrors, inputMirrorLockWait = 2, time.Minute
	t.Cleanup(func() { maxInputMirrors, inputMirrorLockWait = prevMax, prevWait })
	release := holdSecretsStore(t)

	for i := 0; i < 6; i++ {
		mirrorUserInputAsync("capped", "hello")
	}
	inputMirrorMu.Lock()
	inFlight := inputMirrorsInFlight
	inputMirrorMu.Unlock()
	if inFlight != 2 {
		t.Fatalf("in flight = %d, want 2 (the cap)", inFlight)
	}
	release()
	WaitInputMirrors()
	inputMirrorMu.Lock()
	inFlight = inputMirrorsInFlight
	inputMirrorMu.Unlock()
	if inFlight != 0 {
		t.Fatalf("in flight after drain = %d, want 0 (slot leaked)", inFlight)
	}
	if !acquireInputMirrorSlot() {
		t.Fatal("a slot should be free after the mirrors finished")
	}
	releaseInputMirrorSlot()
}
