//go:build !linux

package routegw

import "net"

// peerOf has no SO_PEERCRED to read off Linux, so every caller is refused there.
func peerOf(net.Conn) (procID, bool) { return procID{}, false }
