//go:build linux

package codex

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// statfsIsNFS reports whether path (symlinks followed) is on an NFS mount, which is how EFS
// and the keep volume appear inside the container.
func statfsIsNFS(path string) bool {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return false
	}
	return st.Type == unix.NFS_SUPER_MAGIC
}

// fileID identifies one generation of a file: a database recreated at the same name gets a new
// inode even when size and mtime happen to match.
func fileID(fi os.FileInfo) string {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d:%d", st.Ino, fi.Size(), fi.ModTime().UnixNano())
	}
	return fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano())
}
