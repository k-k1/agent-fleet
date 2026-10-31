package cloudruns

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// holdLock takes dir's spool lock through a descriptor of its own, as a wrapper stopped while
// it writes a row would, and returns the release.
func holdLock(t *testing.T, dir string) func() {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }
}

// within fails the test unless fn returns before d.
func within(t *testing.T, d time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %s while the spool lock was held", what, d)
	}
}

// A held spool lock (a wrapper stopped mid-write) never makes a run wait: Begin, Resolved,
// Refuse and Recent give up after lockWait and write nothing, so no limit is broken without
// the lock. Once it is free, rows are written and the limits hold again.
func TestBusySpoolNeverBlocksARun(t *testing.T) {
	for _, c := range []struct{ name, session string }{{"session", "cr-busy"}, {"workspace", ""}} {
		t.Run(c.name, func(t *testing.T) {
			dir := useRoot(t)
			liveSession(t, "cr-busy")
			spool := filepath.Join(dir, "cr-busy")
			if c.session == "" {
				spool = filepath.Join(dir, workspaceDir)
			}
			release := holdLock(t, spool)
			limit := 2 * time.Second
			var r *Run
			within(t, limit, "Begin", func() { r = Begin("aws", c.session, "p", []string{"aws"}) })
			within(t, limit, "Resolved", func() { r.Resolved(Identity{Account: "123456789012"}) })
			within(t, limit, "Refuse", func() { r.Refuse(RefusedOther) })
			within(t, limit, "Recent", func() { Recent(10) })
			if n := len(rowFiles(spool)); n != 0 {
				t.Fatalf("%d rows written without the lock", n)
			}
			release()

			max := perSession
			if c.session == "" {
				max = workspaceMax
			}
			for i := 0; i < max+3; i++ {
				Begin("aws", c.session, "p", []string{"aws"})
			}
			if n := len(rowFiles(spool)); n != max {
				t.Fatalf("after the lock was freed: %d rows, want %d", n, max)
			}
		})
	}
}

// The workspace spool's lock held by one unattributed run does not stall a run of a session.
func TestBusyWorkspaceSpoolDoesNotStallASession(t *testing.T) {
	dir := useRoot(t)
	liveSession(t, "cr-other")
	defer holdLock(t, filepath.Join(dir, workspaceDir))()
	within(t, 2*time.Second, "Begin of a session's run", func() { Begin("aws", "cr-other", "p", []string{"aws"}) })
	if n := len(ForSession("cr-other")); n != 1 {
		t.Fatalf("session rows = %d, want 1", n)
	}
}

// An expired workspace row is not listed even when the prune could not take the lock.
func TestRecentHidesExpiredRowsWithoutTheLock(t *testing.T) {
	dir := useRoot(t)
	Begin("aws", "", "p", []string{"aws"})
	clock := now()
	now = func() time.Time { return clock.Add(workspaceKeep + time.Hour) }
	defer holdLock(t, filepath.Join(dir, workspaceDir))()
	if n := len(Recent(10)); n != 0 {
		t.Fatalf("Recent lists %d expired rows", n)
	}
}
