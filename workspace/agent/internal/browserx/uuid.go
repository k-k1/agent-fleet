package browserx

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// randUUID returns a random RFC-4122 v4 UUID. newBrowserHandoffID takes 10 hex
// digits out of it for the ledger row id.
//
// This is a copy of the identically named function in package main's chat_store.go: the
// original sits in a file this package does not own, so it could not be moved. Borrowing
// it through a function variable was rejected too — it would be nil in browserx's own
// test binary, so a test that merely mints an id would fail for being "not wired up",
// and eight lines of randomness do not justify special-casing how the tests run.
// Fold the duplicate into a shared util package when the aliases are reclaimed.
func randUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}
