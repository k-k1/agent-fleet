//go:build linux

package runtime

import (
	"os"
	"path/filepath"
	"syscall"
)

// mountPoint reports whether dir is the root of a mount: it sits on another device than
// its parent. Enough for the task's one mount, which is never the container's own root.
func mountPoint(dir string) (bool, error) {
	var self, parent syscall.Stat_t
	if err := syscall.Lstat(dir, &self); err != nil {
		return false, err
	}
	if err := syscall.Lstat(filepath.Dir(dir), &parent); err != nil {
		return false, err
	}
	if self.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return false, &os.PathError{Op: "mountpoint", Path: dir, Err: syscall.ENOTDIR}
	}
	return self.Dev != parent.Dev, nil
}
