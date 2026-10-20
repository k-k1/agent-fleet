//go:build !linux

package cursor

import "errors"

// exchange is unavailable: without an atomic swap an existing hooks.json cannot be replaced
// without a window in which a save of the user's is overwritten, so EnsureHooks gives up on it.
func exchange(a, b string) error { return errors.ErrUnsupported }
