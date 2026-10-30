//go:build linux

package egressagent

import (
	"encoding/binary"
	"net"
	"net/netip"

	"golang.org/x/sys/unix"
)

// origDst reads the destination conntrack recorded before the nftables redirect
// (SO_ORIGINAL_DST, which returns a struct sockaddr_in).
func origDst(c *net.TCPConn) (netip.AddrPort, error) {
	rc, err := c.SyscallConn()
	if err != nil {
		return netip.AddrPort{}, err
	}
	var m *unix.IPv6Mreq
	var serr error
	if err := rc.Control(func(fd uintptr) {
		m, serr = unix.GetsockoptIPv6Mreq(int(fd), unix.SOL_IP, unix.SO_ORIGINAL_DST)
	}); err != nil {
		return netip.AddrPort{}, err
	}
	if serr != nil {
		return netip.AddrPort{}, serr
	}
	return sockaddrIn4(m.Multiaddr), nil
}

// sockaddrIn4 decodes a struct sockaddr_in: family, port (network order), address.
func sockaddrIn4(b [16]byte) netip.AddrPort {
	return netip.AddrPortFrom(netip.AddrFrom4([4]byte(b[4:8])), binary.BigEndian.Uint16(b[2:4]))
}
