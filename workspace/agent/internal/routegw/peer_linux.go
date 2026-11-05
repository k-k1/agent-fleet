//go:build linux

package routegw

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerOf pins the connecting process: its pid from the kernel (SO_PEERCRED, taken at connect
// and not forgeable by the caller) and its start time, read so that it provably belongs to that
// process (peerStart). Only a Unix socket has a peer.
func peerOf(c net.Conn) (procID, bool) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return procID{}, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return procID{}, false
	}
	var id procID
	var got bool
	if err := raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil || cred == nil || cred.Pid <= 1 {
			return
		}
		id.pid = int(cred.Pid)
		id.start, got = peerStart(int(fd), id.pid)
	}); err != nil || !got {
		return procID{}, false
	}
	return id, true
}
