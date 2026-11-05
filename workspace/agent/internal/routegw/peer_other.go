//go:build !linux

package routegw

import "net"

// peerPID has no SO_PEERCRED to read off Linux, so every caller is refused there.
func peerPID(net.Conn) (int, bool) { return 0, false }
