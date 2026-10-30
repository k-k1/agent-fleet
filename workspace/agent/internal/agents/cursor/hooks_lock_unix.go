//go:build unix

package cursor

import (
	"os"
	"syscall"
)

// tryFlock takes an exclusive advisory lock on f without blocking; the kernel drops it when the
// holder exits.
func tryFlock(f *os.File) bool {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}

// lockSupported says tryFlock really excludes other processes.
const lockSupported = true
