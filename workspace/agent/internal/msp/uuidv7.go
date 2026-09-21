package msp

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// NewCommandID mints the UUIDv7 every MSP command carries as its idempotency handle. The
// schema is explicit that the server never mints one, and that a retry re-sending the same
// handle is deduplicated into the original's result rather than run twice — so a caller that
// retries must reuse the id it already sent, not call this again.
//
// It is also what `session/start.sessionId` takes: measured, a v7 we minted was used verbatim
// on the wire and in the on-disk path. AF's usual deterministic UUIDv5 cannot be used there,
// because a retained or reserved id is refused `session_id_conflict` — an id is good once
// (ADR 0095 decision 4).
func NewCommandID() string { return uuidV7(time.Now()) }

// uuidV7 renders RFC 9562 version 7: 48 bits of Unix milliseconds, then random bits, with the
// version and variant nibbles set. Time-ordered by construction, which is why the schema
// picked it — ids sort into the order the commands were issued.
func uuidV7(now time.Time) string {
	var u [16]byte
	ms := now.UnixMilli()
	u[0] = byte(ms >> 40)
	u[1] = byte(ms >> 32)
	u[2] = byte(ms >> 24)
	u[3] = byte(ms >> 16)
	u[4] = byte(ms >> 8)
	u[5] = byte(ms)
	// rand.Read from crypto/rand never returns a short read or an error on any platform this
	// runs on; it panics internally if the OS source fails, which is the right outcome for an
	// id that must not repeat.
	rand.Read(u[6:])
	u[6] = (u[6] & 0x0f) | 0x70 // version 7
	u[8] = (u[8] & 0x3f) | 0x80 // variant RFC 4122
	return formatUUID(u)
}

func formatUUID(u [16]byte) string {
	var b [36]byte
	hex.Encode(b[0:8], u[0:4])
	b[8] = '-'
	hex.Encode(b[9:13], u[4:6])
	b[13] = '-'
	hex.Encode(b[14:18], u[6:8])
	b[18] = '-'
	hex.Encode(b[19:23], u[8:10])
	b[23] = '-'
	hex.Encode(b[24:36], u[10:16])
	return string(b[:])
}
