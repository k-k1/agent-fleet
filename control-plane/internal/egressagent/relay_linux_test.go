//go:build linux

package egressagent

import (
	"net/netip"
	"testing"
)

func TestSockaddrIn4(t *testing.T) {
	// AF_INET (2, host order), port 443 (network order), 203.0.113.7, zero padding.
	b := [16]byte{2, 0, 0x01, 0xbb, 203, 0, 113, 7}
	if got := sockaddrIn4(b); got != netip.MustParseAddrPort("203.0.113.7:443") {
		t.Errorf("sockaddrIn4 = %s", got)
	}
}
