package runtime

import (
	"crypto/rand"
	"encoding/hex"
)

// newTestID is a unique suffix for a resource a test creates. The CP's newID (store.go)
// used to serve here; this package cannot see it, and the tests only ever needed
// "different every time".
func newTestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
