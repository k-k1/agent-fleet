//go:build !unix

package egressagent

import "os"

// inode is 0 where the platform has none; the gateway and the first line still identify
// the file. The gateway runs Linux.
func inode(os.FileInfo) uint64 { return 0 }
