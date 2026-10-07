package status

import (
	"os"
	"path/filepath"
	"time"
)

// lockWait bounds how long a session-status writer waits for the per-sid lock. The Stop hook
// is on claude's critical path (its latency delays the TUI's turn end), and the lock is held
// for one temp+rename write, so the wait is normally microseconds; this is the ceiling for a
// pathological holder. Var so tests can shorten it.
var lockWait = 300 * time.Millisecond

const lockPoll = 2 * time.Millisecond

// lockDir sits next to the status files. It is a subdirectory, like fstore.StagingSubdir, so
// walkers that list the store never see a lock file.
func lockDir() string { return filepath.Join(statusFiles.Dir(), ".locks") }

func lockPath(sid string) string { return filepath.Join(lockDir(), sid+".lock") }

// lockSid takes the inter-process lock that serialises the writers of sid's status record
// (the hook's persist and the pane reverse-heal's conditional write). It returns an unlock
// func and whether the lock was obtained within lockWait. Callers that hold the authoritative
// record (the hook) write anyway on false; the heal skips.
//
// One lock file per sid, removed with the record (Remove), which unlinks it only while
// holding the lock. A waiter that opened the old inode re-checks, after locking, that the path
// still names the file it holds and retries otherwise. Every retry path honours the deadline.
func lockSid(sid string) (unlock func(), ok bool) {
	deadline := time.Now().Add(lockWait)
	if err := os.MkdirAll(lockDir(), 0o700); err != nil {
		return func() {}, false
	}
	for {
		f, err := os.OpenFile(lockPath(sid), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return func() {}, false
		}
		if tryFlock(f) {
			same, err := sameFile(f, lockPath(sid))
			if err != nil {
				_ = f.Close() // a persistent Stat error is not retried: give up within the bound
				return func() {}, false
			}
			if same {
				return func() { _ = f.Close() }, true // closing releases the flock
			}
			// Unlinked by Remove while we waited: lock the new file, within the deadline.
		}
		_ = f.Close()
		if time.Now().After(deadline) {
			return func() {}, false
		}
		time.Sleep(lockPoll)
	}
}

// sameFile reports whether path still names the file f holds. A missing path is "not the
// same" (Remove unlinked it); any other Stat error is returned.
func sameFile(f *os.File, path string) (bool, error) {
	a, err := f.Stat()
	if err != nil {
		return false, err
	}
	b, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return os.SameFile(a, b), nil
}

// afterCheck runs inside PersistIf between its check and its write; tests stall it.
var afterCheck = func() {}

// PersistIf writes {state} for sid only if the record is still the one the caller decided
// from: wasRev is its Rev and existed whether there was a record at all. Under the per-sid
// lock it re-reads the record, so a closed turn the Stop hook (another process) persisted
// since the caller's read is never overwritten. It reports whether it wrote; a lock timeout
// counts as not written.
func PersistIf(sid, state string, wasRev string, existed bool) bool {
	unlock, ok := lockSid(sid)
	defer unlock()
	if !ok {
		return false
	}
	acquired := time.Now()
	st, has := Read(sid)
	if has != existed || st.Rev != wasRev {
		return false
	}
	afterCheck()
	// The hook gives up waiting after lockWait and writes without the lock, so a holder that
	// has been stalled (descheduled, slow I/O) longer than half of that may no longer be
	// excluding it. Give way rather than commit on a check that old. What remains is a stall
	// of more than lockWait/2 inside the few instructions between this test and the rename.
	if time.Since(acquired) > lockWait/2 {
		return false
	}
	// persistLocked, not Persist: the lock is not reentrant.
	persistLocked(sid, SessionStatus{State: state})
	if state == "working" {
		RemoveCompletionKey(sid)
	}
	return true
}
