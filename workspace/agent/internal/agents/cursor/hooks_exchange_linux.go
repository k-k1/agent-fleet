//go:build linux

package cursor

import "golang.org/x/sys/unix"

// exchange atomically swaps the two paths (renameat2 RENAME_EXCHANGE): neither name is ever
// missing, and what each held ends up under the other's name. It fails (EINVAL) on filesystems
// that cannot do it.
func exchange(a, b string) error {
	return unix.Renameat2(unix.AT_FDCWD, a, unix.AT_FDCWD, b, unix.RENAME_EXCHANGE)
}
