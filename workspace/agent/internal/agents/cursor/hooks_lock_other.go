//go:build !unix

package cursor

import "os"

// lockSupported is false where flock is unavailable: EnsureHooks gives up rather than let two
// agent processes race each other over the user's file.
const lockSupported = false

func tryFlock(*os.File) bool { return false }
