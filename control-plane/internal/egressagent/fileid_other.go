//go:build !linux

package egressagent

import (
	"errors"
	"os"
)

// fileGen needs Linux extended attributes; the gateway runs Linux.
func fileGen(*os.File) (string, error) {
	return "", errors.New("egress-agent identifies log files by an extended attribute and runs on Linux only")
}
