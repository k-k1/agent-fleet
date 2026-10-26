//go:build !linux

package egressagent

import (
	"errors"
	"net"
	"net/netip"
)

// origDst needs Linux conntrack; the gateway runs Linux.
func origDst(*net.TCPConn) (netip.AddrPort, error) {
	return netip.AddrPort{}, errors.New("egress-relay reads the original destination from Linux conntrack and runs on Linux only")
}
