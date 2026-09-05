package browserx

// The host capabilities browserx borrows from package main. They come in as function
// variables rather than imports: the providers live in files browserx must not depend on
// (the loopback REST delivery stack in mcp_stdio.go, the chromium pin resolution in
// install_tools.go), and importing package main back is impossible anyway.
//
// The names are the same as before the move, so the calls in the moved files did not
// change by a single character. Wiring happens exactly once, in the init of package main's
// alias_browser.go.
//
// Calling one while it is still nil panics. A harmless default would let a test pass green
// with the wiring forgotten, so a missing wire fails loudly instead. The browserx-only
// test binary wires them explicitly in deps_test.go's TestMain.
var (
	// agentSendToSession delivers a prompt to a session over the loopback Agent REST
	// (resuming a stopped one first, then re-sending). Used by the handoff ledger's
	// delivery stage.
	agentSendToSession func(name string, body []byte) (out string, resumed bool, err error)

	// chromiumDefaultPin returns the chromium build this architecture is pinned to.
	chromiumDefaultPin func() string

	// chromiumPinnedBinary returns the pinned chromium executable ("" when not installed).
	chromiumPinnedBinary func() string

	// installChromium fetches and lays down the chromium of the given pin.
	installChromium func(pin string) error
)

// SetDeps wires the host capabilities up. Called once, from package main's init.
func SetDeps(
	sendToSession func(name string, body []byte) (string, bool, error),
	defaultPin func() string,
	pinnedBinary func() string,
	install func(pin string) error,
) {
	agentSendToSession = sendToSession
	chromiumDefaultPin = defaultPin
	chromiumPinnedBinary = pinnedBinary
	installChromium = install
}
