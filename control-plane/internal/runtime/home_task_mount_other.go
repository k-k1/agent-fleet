//go:build !linux

package runtime

import "errors"

// mountPoint: the home-ops task only ever runs on Linux (Fargate).
func mountPoint(string) (bool, error) {
	return false, errors.New("mount detection is implemented for Linux only")
}
