//go:build linux

package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// nfsSuperMagic is NFS_SUPER_MAGIC, the f_type statfs reports for an NFS mount (EFS is
// NFSv4.1).
const nfsSuperMagic = 0x6969

// efsMount reports whether dir is the root of an NFS mount: another device than its
// parent, and an NFS file system. A device boundary alone is any mount — a tmpfs left at
// the path would pass it, the removal would run on that instead, and an empty one would
// report the home cleaned. Which NFS export it is (the deployment's EFS, at its root) is
// the task definition's to guarantee: HomeOpsTaskDef mounts that file system with
// RootDirectory "/" and nothing else.
func efsMount(dir string) (bool, error) {
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
	if self.Dev == parent.Dev {
		return false, nil
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(dir, &fs); err != nil {
		return false, err
	}
	if fs.Type != nfsSuperMagic {
		return false, fmt.Errorf("%s is a mount of file system type %#x, not NFS", dir, fs.Type)
	}
	return true, nil
}
