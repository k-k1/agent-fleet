//go:build !unix

package egressagent

import "os"

// fileID has nothing to offer where there are no inodes; the gateway runs Linux.
func fileID(*os.File) (uint64, int64, error) { return 0, 0, nil }
