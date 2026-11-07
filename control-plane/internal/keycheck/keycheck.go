// Package keycheck judges the strength of the deployment secrets, AF_MASTER_KEY and
// AF_COOKIE_SECRET.
package keycheck

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// This release only warns (log line + deployment_warnings); refusing to start under AUTH other
// than dev is a later release, so nothing here may be wired to log.Fatal yet.
//
// The rule: the value must be at least 32 bytes of key material, either
//   - hex or base64 text that decodes to >= minKeyBytes bytes, or
//   - any other string of >= minRawKeyChars characters.
//
// 43 characters is the length of the shortest unpadded base64 text of 32 bytes, so a raw
// value is held to the same text length a generated one has. Length is all this can
// measure: it cannot tell a long passphrase from random bytes, which is why the guide says
// to generate the value. A value with only a handful of distinct bytes (a repeated
// placeholder) is refused whatever its length.
//
// Nothing in this file logs, returns or stores the value or a digest of it; callers get a
// bool.
const (
	minKeyBytes    = 32
	minRawKeyChars = 43
	// minDistinctBytes separates repetition ("aaaa…", "changeme" × n) from key material:
	// 32 random bytes hold about 28 distinct values.
	minDistinctBytes = 8
)

// Weak reports whether s is set but does not meet the rule above. An empty value
// is not weak: an unset key has its own handling (plaintext_secrets, the oauth config check).
// A non-empty value of only whitespace is weak: AF_MASTER_KEY of that kind is still hashed and
// switches the custodian on, so it must not read as unset here.
func Weak(s string) bool {
	if s == "" {
		return false
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	b, ok := decodedBytes(s)
	if !ok {
		b = []byte(s)
		if len(b) < minRawKeyChars {
			return true
		}
	} else if len(b) < minKeyBytes {
		return true
	}
	seen := map[byte]struct{}{}
	for _, c := range b {
		seen[c] = struct{}{}
	}
	return len(seen) < minDistinctBytes
}

// decodedBytes decodes s as hex, else base64 (std or url, padded or raw). Hex goes first:
// a hex string is also valid base64, and reading it as base64 would credit it with 50% more
// bytes than it carries.
func decodedBytes(s string) ([]byte, bool) {
	if len(s)%2 == 0 {
		if b, err := hex.DecodeString(s); err == nil {
			return b, true
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, true
		}
	}
	return nil, false
}
