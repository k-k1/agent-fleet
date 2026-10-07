//go:build unix

package status

import (
	"os"
	"syscall"
)

// tryFlock takes an exclusive advisory lock on f without blocking. The kernel drops it when
// the holder exits, so a hook killed mid-write cannot leave the record locked.
func tryFlock(f *os.File) bool {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}
