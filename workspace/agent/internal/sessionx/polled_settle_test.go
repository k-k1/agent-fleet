package sessionx

// The settle window of recordPolledTurnEnd is off for the package's tests, which write a turn's
// start and end back to back; the tests that are about the window turn it on.
func init() { polledEndSettle = 0 }
