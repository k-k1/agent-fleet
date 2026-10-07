//go:build !unix

package status

import "os"

// tryFlock is a no-op where flock is unavailable: the conditional write degrades to the
// Rev check alone, which narrows the race without closing it.
func tryFlock(*os.File) bool { return true }
