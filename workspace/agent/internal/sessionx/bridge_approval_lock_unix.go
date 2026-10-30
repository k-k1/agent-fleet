//go:build unix

package sessionx

import (
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// lockApprovals takes the exclusive advisory lock that serializes the daemon's
// read-check-write of a decision against the waiter's deadline removal. The two run in
// different processes, so only a file lock closes the gap. The kernel drops it when the
// holder exits, so a killed process cannot leave it held. The wait is bounded
// (bridgeApprovalLockWait): a stopped or wedged holder must not push the waiter past the
// approval window, so the caller gets an error and decides how to fail safe.
func lockApprovalsFile() (unlock func(), err error) {
	dir := bridgeApprovals.Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	giveUp := time.Now().Add(bridgeApprovalLockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = f.Close() }, nil // closing releases the flock
		}
		if (err != syscall.EWOULDBLOCK && err != syscall.EINTR) || time.Now().After(giveUp) {
			_ = f.Close()
			return nil, err
		}
		time.Sleep(5 * time.Millisecond)
	}
}
