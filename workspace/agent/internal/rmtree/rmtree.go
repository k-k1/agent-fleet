// Package rmtree removes a directory tree that holds directories the owner cannot write to.
package rmtree

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// RemoveAll is os.RemoveAll that also gets through read-only directories. A tool may leave
// them behind (Go's module cache is mode 0555), and os.RemoveAll cannot unlink inside one, so
// without this the removal fails with EACCES on every attempt and the folder stays for good.
//
// Only a permission failure triggers the second pass: every directory under p gets owner
// read, write and search, then the removal is retried. The walk does not follow symlinks (a
// link is neither descended into nor chmod'ed), so nothing outside p is ever touched. Files
// are never chmod'ed: unlinking needs write on the parent directory, not on the file.
func RemoveAll(p string) error {
	err := os.RemoveAll(p)
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		return err
	}
	_ = filepath.WalkDir(p, func(q string, d fs.DirEntry, werr error) error {
		if werr != nil || !d.IsDir() { // d.IsDir() is false for a symlink, even one to a directory
			return nil
		}
		// The callback runs before WalkDir reads the directory, so a 0300 or 0000 one is
		// opened up in time to be listed.
		if fi, serr := os.Lstat(q); serr == nil && fi.IsDir() {
			_ = os.Chmod(q, fi.Mode().Perm()|0o700)
		}
		return nil
	})
	return os.RemoveAll(p)
}
