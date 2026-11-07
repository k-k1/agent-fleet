// Package errref turns an unexpected server-side error into a short correlation ID.
//
// A 500 body must not carry the error text: cloud SDK, KMS and database errors name
// resources, internal endpoints and SQL fragments. The caller gets a fixed message plus
// the ID, and the original error goes to the operator's log under the same ID.
package errref

import (
	"crypto/rand"
	"encoding/hex"
	"log"
)

// Message is what a caller sees for an unexpected failure.
func Message(ref string) string { return "internal error (ref: " + ref + ")" }

// Log records err under a fresh reference and returns the reference. The text is quoted:
// it can come from outside (a database or cloud API reply) and must not split the entry.
func Log(err error) string {
	ref := newRef()
	text := "<nil>"
	if err != nil {
		text = err.Error()
	}
	log.Printf("internal error ref=%s: %q", ref, text)
	return ref
}

func newRef() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(b[:])
}
