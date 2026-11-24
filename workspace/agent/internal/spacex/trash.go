package spacex

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// lifeMu orders the two kinds of work on a space. Writing inside one (a rename of its marker, an
// attachment copied in) holds it shared; taking the whole folder out of ~/spaces or putting it
// back (MoveOut, MoveIn) holds it exclusively. Without it a copy that is in flight when the space
// is moved lands in the moved folder, and the paste answers a path that is no longer under
// ~/spaces. Lock order: renameMu, then lifeMu.
var lifeMu sync.RWMutex

// ErrExists is what MoveIn answers when a folder of that name is already under Root.
var ErrExists = errors.New("something already exists at that space folder")

// SameDevice reports whether a and b are on one file system, so a rename from one to the other
// is a rename and not a copy. Both paths have to exist.
func SameDevice(a, b string) (bool, error) {
	var sa, sb syscall.Stat_t
	if err := syscall.Stat(a, &sa); err != nil {
		return false, err
	}
	if err := syscall.Stat(b, &sb); err != nil {
		return false, err
	}
	return sa.Dev == sb.Dev, nil
}

// MoveOut moves the space in folder to dest, which must not exist yet. It is the whole of
// deleting a space's files: a rename, so nothing is copied and nothing is lost half way, and the
// folder, its marker and every file in it come back unchanged through MoveIn. A folder that is no
// space (never made, a symlink, no marker) is ErrNotFound. The rename fails across file systems
// (EXDEV); callers check SameDevice first so that is found out before anything else is changed.
func MoveOut(folder, dest string) error {
	if !ValidFolder(folder) {
		return ErrNotFound
	}
	lifeMu.Lock()
	defer lifeMu.Unlock()
	dir := filepath.Join(Root(), folder)
	if !markerPresent(dir) {
		return ErrNotFound
	}
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("%s already exists", dest)
	}
	return os.Rename(dir, dest)
}

// MoveIn puts a folder MoveOut moved back at Root/folder. It refuses (ErrExists) when anything,
// even an empty directory, is there: a rename onto an empty directory would succeed and replace
// it. src has to be a real directory that carries the marker, so a trash entry that was emptied
// or replaced is not turned into a space.
func MoveIn(src, folder string) error {
	if !ValidFolder(folder) {
		return ErrNotFound
	}
	lifeMu.Lock()
	defer lifeMu.Unlock()
	if !markerPresent(src) {
		return ErrNotFound
	}
	root := Root()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	realRoots.Delete(root) // a root remembered as missing exists now
	dir := filepath.Join(root, folder)
	if _, err := os.Lstat(dir); err == nil {
		return ErrExists
	}
	return os.Rename(src, dir)
}
