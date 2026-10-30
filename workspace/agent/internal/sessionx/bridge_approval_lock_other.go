//go:build !unix

package sessionx

// lockApprovals is a no-op where flock is unavailable: the timeout race narrows to the
// window between the daemon's read and write but is not closed.
func lockApprovals() (func(), error) { return func() {}, nil }
