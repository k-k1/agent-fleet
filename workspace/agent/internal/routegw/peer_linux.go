//go:build linux

package routegw

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerPID reads the connecting process from the kernel. Only a Unix socket has one.
func peerPID(c net.Conn) (int, bool) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var cred *unix.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || cerr != nil || cred == nil {
		return 0, false
	}
	return int(cred.Pid), true
}
