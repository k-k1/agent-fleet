//go:build !contract

package opencode

import "os"

// pinNoHostOpencode keeps a unit test off the opencode the host happens to have: with none on
// PATH and no pin, the unprobed major is 1.x, and a test that wants 2.x says so. The contract
// build runs the real CLI and keeps the real resolvers.
func pinNoHostOpencode() {
	opencodeBinary = func() string { return "" }
	buildPinsPath = os.DevNull
}
