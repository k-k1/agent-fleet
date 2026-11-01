package sessionx

import (
	"errors"
	"time"
)

// approvalMemLock serializes the decision write within this process. It is untagged so the
// non-unix lock and the tests share one implementation; the unix flock already excludes
// goroutines too (each acquisition opens its own file description), so it does not use it.
// It is a channel rather than a sync.Mutex because the wait must be bounded.
var approvalMemLock = make(chan struct{}, 1)

var errApprovalLockBusy = errors.New("approval lock busy")

// lockApprovalsInProcess takes approvalMemLock, giving up after bridgeApprovalLockWait so a
// wedged holder cannot push the caller past the approval window. The caller releases on
// every path; the holder never waits for the user inside the section.
func lockApprovalsInProcess() (unlock func(), err error) {
	t := time.NewTimer(bridgeApprovalLockWait)
	defer t.Stop()
	select {
	case approvalMemLock <- struct{}{}:
		return func() { <-approvalMemLock }, nil
	case <-t.C:
		return nil, errApprovalLockBusy
	}
}
