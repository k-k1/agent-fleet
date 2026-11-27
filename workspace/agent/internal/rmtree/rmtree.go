// Package rmtree removes a directory tree that holds directories the owner cannot write to.
package rmtree

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// RemoveAll is os.RemoveAll that also gets through read-only directories. A tool may leave
// them behind (Go's module cache is mode 0555), and os.RemoveAll cannot unlink inside one, so
// without this the removal fails with EACCES on every attempt and the folder stays for good.
//
// Only a permission failure triggers the second pass: every directory under p gets owner
// read, write and search, then the removal is retried. The walk does not follow symlinks (a
// link is neither descended into nor chmod'ed). A directory on another device (a mount) is
// never opened up, and a tree that holds one is not retried at all: the retry's os.RemoveAll
// would walk into the mount, and what lives behind it is not the target's to delete. The first
// error is returned instead, as before. A bind mount from the same device cannot be told apart
// by device number and is not detected. Files are never chmod'ed: unlinking needs write on the
// parent directory, not on the file.
//
// Not a defence against a process that rewrites the tree while it is being removed: the
// chmod goes by path, so a directory swapped for a symlink between the check and the chmod
// would be followed. Callers hold the folder out of use first (the delete mark, the trash).
func RemoveAll(p string) error {
	p = filepath.Clean(p)
	parent, err := os.OpenRoot(filepath.Dir(p))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer parent.Close()
	return RemoveAllIn(parent, filepath.Base(p))
}

// RemoveAllIn applies the read-only directory retry relative to an already opened parent.
func RemoveAllIn(parent *os.Root, name string) error {
	err := parent.RemoveAll(name)
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		return err
	}
	var root uint64
	if fi, serr := parent.Lstat(name); serr == nil && fi.IsDir() {
		root = devOf(fi)
	} else {
		return err
	}
	crossed := false
	_ = fs.WalkDir(parent.FS(), name, func(q string, d fs.DirEntry, werr error) error {
		if werr != nil || !d.IsDir() { // d.IsDir() is false for a symlink, even one to a directory
			return nil
		}
		// The callback runs before WalkDir reads the directory, so a 0300 or 0000 one is
		// opened up in time to be listed.
		fi, serr := parent.Lstat(q)
		if serr != nil || !fi.IsDir() {
			return nil
		}
		if devOf(fi) != root {
			crossed = true
			return fs.SkipDir // a mount point: leave it and what it holds alone
		}
		_ = parent.Chmod(q, fi.Mode().Perm()|0o700)
		return nil
	})
	if crossed {
		return err
	}
	return parent.RemoveAll(name)
}

// devOf is the device a file lives on; a seam so a test can stand in for a mount point, which
// an unprivileged test cannot create. A file whose device cannot be read counts as device 0.
var devOf = func(fi fs.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev)
	}
	return 0
}
