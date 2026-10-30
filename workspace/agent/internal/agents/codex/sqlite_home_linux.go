//go:build linux

package codex

import "golang.org/x/sys/unix"

// statfsIsNFS reports whether path (symlinks followed) is on an NFS mount, which is how EFS
// and the keep volume appear inside the container.
func statfsIsNFS(path string) bool {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return false
	}
	return st.Type == unix.NFS_SUPER_MAGIC
}
