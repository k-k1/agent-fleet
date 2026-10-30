//go:build unix

package sessionx

import (
	"os"
	"path/filepath"
	"syscall"
)

// lockApprovals takes the exclusive advisory lock that serializes the daemon's
// read-check-write of a decision against the waiter's deadline removal. The two run in
// different processes, so only a file lock closes the gap. The kernel drops it when the
// holder exits, so a killed process cannot leave it held. Critical sections are a single
// small read/write, so the blocking wait is brief.
func lockApprovals() (unlock func(), err error) {
	dir := bridgeApprovals.Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = f.Close() }, nil // closing releases the flock
}
