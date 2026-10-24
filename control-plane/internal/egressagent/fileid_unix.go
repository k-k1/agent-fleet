//go:build unix && !linux

package egressagent

import (
	"os"
	"syscall"
)

// fileID is the inode only: the gateway runs Linux, and this exists so the package builds
// and its tests run on a developer's machine.
func fileID(f *os.File) (uint64, int64, error) {
	fi, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino), 0, nil
	}
	return 0, 0, nil
}
