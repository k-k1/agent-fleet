//go:build linux

package egressagent

import (
	"os"

	"golang.org/x/sys/unix"
)

// fileID is the file's inode and birth time in nanoseconds (0 where the filesystem does
// not record one). The birth time is what tells a new file from a deleted one whose
// inode it reuses.
func fileID(f *os.File) (ino uint64, btime int64, err error) {
	var st unix.Statx_t
	if err := unix.Statx(int(f.Fd()), "", unix.AT_EMPTY_PATH, unix.STATX_INO|unix.STATX_BTIME, &st); err != nil {
		return 0, 0, err
	}
	if st.Mask&unix.STATX_BTIME != 0 {
		btime = st.Btime.Sec*1e9 + int64(st.Btime.Nsec)
	}
	return st.Ino, btime, nil
}
