//go:build linux

package imagegen

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// readBeneath reads root/rel with the root fixed as a descriptor and every component below it
// resolved with RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS — the same open the Files pane's reader uses
// (fs_fd_linux.go's openat2NoSymlinks), so a symlink anywhere under the root, or a ".." that
// climbs out of it, fails the open itself instead of a check that ran before it.
//
// O_NONBLOCK keeps a FIFO planted under the name from hanging the request; the type check
// afterwards refuses it. max <= 0 means no ceiling.
func readBeneath(root, rel string, max int64) ([]byte, error) {
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open root: %w", err)
	}
	defer unix.Close(rootFD)
	fd, err := unix.Openat2(rootFD, rel, &unix.OpenHow{
		Flags:   uint64(unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, errors.New("symlinks are not followed")
		}
		if errors.Is(err, unix.EXDEV) {
			return nil, errors.New("the path leaves its root")
		}
		return nil, err
	}
	f := os.NewFile(uintptr(fd), rel)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, errors.New("not a regular file")
	}
	if max > 0 && st.Size > max {
		return nil, fmt.Errorf("%d bytes, over the %d-byte limit for one picture", st.Size, max)
	}
	var r io.Reader = f
	if max > 0 {
		r = io.LimitReader(f, max+1)
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if max > 0 && int64(len(raw)) > max {
		return nil, fmt.Errorf("over the %d-byte limit for one picture", max)
	}
	return raw, nil
}
