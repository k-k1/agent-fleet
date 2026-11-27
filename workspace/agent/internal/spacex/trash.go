package spacex

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// lifeMu orders the two kinds of work on a space. Writing inside one (a rename of its marker, an
// attachment copied in) holds it shared; taking the whole folder out of ~/spaces or putting it
// back (MoveOut, MoveIn) holds it exclusively. Without it a copy that is in flight when the space
// is moved lands in the moved folder, and the paste answers a path that is no longer under
// ~/spaces. Lock order: renameMu, then lifeMu.
var lifeMu sync.RWMutex

// ErrExists is what MoveIn answers when a folder of that name is already under Root.
var ErrExists = errors.New("something already exists at that space folder")

// SameDevice reports whether dir and an opened trash root are on one filesystem.
func SameDevice(dir string, trash *os.Root) (bool, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(dir, &st); err != nil {
		return false, err
	}
	info, err := trash.Stat(".")
	if err != nil {
		return false, err
	}
	return st.Dev == info.Sys().(*syscall.Stat_t).Dev, nil
}

// TrashIdentity identifies a space directory across a rename. On file systems without birth
// time, the original marker's identity also has to match before an interrupted restore resumes.
type TrashIdentity struct {
	Inode        uint64 `json:"inode"`
	Birth        int64  `json:"birth,omitempty"`
	MarkerInode  uint64 `json:"markerInode,omitempty"`
	MarkerChange int64  `json:"markerChange,omitempty"`
}

// trashDirectoryID lets tests exercise the fallback on file systems with a birth time.
var trashDirectoryID = statxID

// ReadTrashIdentity requires a real directory and regular marker, using the opened parent.
func ReadTrashIdentity(parent *os.Root, name string) (TrashIdentity, error) {
	d, err := parent.Open(".")
	if err != nil {
		return TrashIdentity{}, err
	}
	defer d.Close()
	return readTrashIdentity(d, name)
}

func readTrashIdentity(parent *os.File, name string) (TrashIdentity, error) {
	if !filepath.IsLocal(name) || filepath.Base(name) != name || name == "." {
		return TrashIdentity{}, ErrNotFound
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ELOOP) {
		return TrashIdentity{}, ErrNotFound
	}
	if err != nil {
		return TrashIdentity{}, err
	}
	defer unix.Close(fd)
	var marker unix.Stat_t
	if err := unix.Fstatat(fd, MarkerFile, &marker, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return TrashIdentity{}, ErrNotFound
		}
		return TrashIdentity{}, err
	}
	if marker.Mode&unix.S_IFMT != unix.S_IFREG {
		return TrashIdentity{}, ErrNotFound
	}
	if id, ok := trashDirectoryID(fd, "", unix.AT_EMPTY_PATH); ok {
		return TrashIdentity{Inode: id.ino, Birth: id.born}, nil
	}
	var dir unix.Stat_t
	if err := unix.Fstat(fd, &dir); err != nil {
		return TrashIdentity{}, err
	}
	return TrashIdentity{Inode: dir.Ino, MarkerInode: marker.Ino, MarkerChange: marker.Ctim.Nano()}, nil
}

// MoveOut moves a space into an opened trash directory in one rename. Missing spaces and
// symlinks are refused before any move. Callers check SameDevice before changing session state.
func MoveOut(folder string, trash *os.Root, entry string) error {
	if !ValidFolder(folder) {
		return ErrNotFound
	}
	lifeMu.Lock()
	defer lifeMu.Unlock()
	spaces, err := os.Open(Root())
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	defer spaces.Close()
	if _, err := readTrashIdentity(spaces, folder); err != nil {
		return err
	}
	if _, err := trash.Lstat(entry); !errors.Is(err, fs.ErrNotExist) {
		if err == nil {
			return fmt.Errorf("%s already exists", entry)
		}
		return err
	}
	if err := renameSpace(spaces, folder, trash, entry, false); err != nil {
		return err
	}
	// A later folder of the same name must not inherit this space's marker.
	forget(folder)
	return nil
}

// MoveIn puts the expected directory back. A different entry or an occupied destination
// leaves the source untouched.
func MoveIn(trash *os.Root, entry string, spaces *os.Root, folder string, expected TrashIdentity) error {
	if !ValidFolder(folder) {
		return ErrNotFound
	}
	lifeMu.Lock()
	defer lifeMu.Unlock()
	from, err := trash.Open(".")
	if err != nil {
		return err
	}
	defer from.Close()
	id, err := readTrashIdentity(from, entry)
	if err != nil {
		return err
	}
	if id != expected {
		return ErrNotFound
	}
	if _, err := spaces.Lstat(folder); !errors.Is(err, fs.ErrNotExist) {
		if err == nil {
			return ErrExists
		}
		return err
	}
	if err := renameSpace(from, entry, spaces, folder, true); err != nil {
		return err
	}
	realRoots.Delete(Root())
	dir := filepath.Join(Root(), folder)
	m, _ := readMarker(dir)
	now, _ := idOf(dir)
	if now != (dirID{ino: id.Inode, born: id.Birth}) {
		now = dirID{}
	}
	remember(folder, entryWith(now, m.Name, m.CreatedAt))
	return nil
}

func renameSpace(from *os.File, old string, dest *os.Root, new string, restore bool) error {
	if !filepath.IsLocal(new) || filepath.Base(new) != new || new == "." {
		return ErrNotFound
	}
	to, err := dest.Open(".")
	if err != nil {
		return err
	}
	defer to.Close()
	err = unix.Renameat2(int(from.Fd()), old, int(to.Fd()), new, unix.RENAME_NOREPLACE)
	// Some filesystems lack RENAME_NOREPLACE. The caller has checked for the destination
	// under lifeMu, which still excludes another Agent operation from creating it.
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOSYS) {
		err = unix.Renameat(int(from.Fd()), old, int(to.Fd()), new)
	}
	if restore && errors.Is(err, fs.ErrExist) {
		return ErrExists
	}
	return err
}
