package browserx

import (
	"errors"
	"os"
	"testing"
)

// TestMain wires up deps.go's function variables for the browserx-only test binary.
// In production package main's alias_browser.go does that wiring, but there is no package
// main in this binary, so entering chromium resolution with them still nil panics (deps.go
// deliberately does not silence that).
//
// The chromium variables are wired as "there is no pin". That is not a stand-in, it is the
// truth for this binary:
//   - chromiumPinnedBinary "" = no pinned build installed. findChromiumBinary looks at
//     AF_CHROMIUM_BIN / CHROMIUM_PATH, then PATH, then the pin, then the playwright cache,
//     so live tests find chromium through env and PATH just as they did before the move.
//   - chromiumDefaultPin "" = ensureChromiumForPane returns (false, nil), i.e. "nothing to
//     install". That is also the right answer for keeping a test binary from downloading
//     chromium on its own.
//   - installChromium is therefore unreachable. If it is reached the wiring assumption is
//     broken, so fail loudly instead of quietly succeeding.
//
// agentSendToSession is deliberately not wired here. The two tests that need it replace it
// explicitly through stubAgentInputServer in browser_handoff_ledger_test.go, and the ledger
// tests that exercise the real wiring live in package main (see the header comment there).
// A default here would let a test that forgot to wire it pass silently.
func TestMain(m *testing.M) {
	chromiumDefaultPin = func() string { return "" }
	chromiumPinnedBinary = func() string { return "" }
	installChromium = func(string) error {
		return errors.New("browserx tests do not install chromium (reaching here means the wiring assumption is wrong)")
	}
	os.Exit(m.Run())
}
