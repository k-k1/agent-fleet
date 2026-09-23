//go:build !linux

package imagegen

import "errors"

// readBeneath has no portable equivalent of openat2's RESOLVE_NO_SYMLINKS, and a weaker read
// would be the gate in name only. The Agent itself is linux-only (fs_fd_linux.go).
func readBeneath(root, rel string, max int64) ([]byte, error) {
	return nil, errors.New("reading a reference image needs Linux openat2")
}
