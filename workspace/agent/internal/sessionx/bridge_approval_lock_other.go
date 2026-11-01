//go:build !unix

package sessionx

// lockApprovalsFile is process-local where flock is unavailable. It serializes concurrent
// decisions on one gate inside the Agent (double click, two tabs), which is the single
// daemon case. Exclusion against the waiter in the mcp-stdio subprocess is left to the
// claim file, which needs no lock.
func lockApprovalsFile() (func(), error) { return lockApprovalsInProcess() }
