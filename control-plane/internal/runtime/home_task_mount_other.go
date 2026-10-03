//go:build !linux

package runtime

import "errors"

// efsMount: the home-ops task only ever runs on Linux (Fargate).
func efsMount(string) (bool, error) {
	return false, errors.New("mount detection is implemented for Linux only")
}
