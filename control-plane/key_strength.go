package main

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// Strength check for the two deployment secrets, AF_MASTER_KEY and AF_COOKIE_SECRET. This
// release only warns (log line + deployment_warnings); refusing to start under AUTH other
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

// weakKeyMaterial reports whether s is set but does not meet the rule above. An empty value
// is not weak: an unset key has its own handling (plaintext_secrets, the oauth config check).
func weakKeyMaterial(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	b, ok := decodedKeyBytes(s)
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

// decodedKeyBytes decodes s as hex, else base64 (std or url, padded or raw). Hex goes first:
// a hex string is also valid base64, and reading it as base64 would credit it with 50% more
// bytes than it carries.
func decodedKeyBytes(s string) ([]byte, bool) {
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

// Deployment warning codes for weak keys; the Console renders them as
// admin.deploy_warn_weak_master_key / admin.deploy_warn_weak_cookie_secret.
const (
	deploymentWarnWeakMasterKey    = "weak_master_key"
	deploymentWarnWeakCookieSecret = "weak_cookie_secret"
)

const weakMasterKeyLog = "WARNING: AUTH=%s and AF_MASTER_KEY is shorter than 32 bytes of key material (or is a repeated pattern). " +
	"Per-member keys derive from it, so it is only as strong as the value chosen. Generate one with `head -c 32 /dev/urandom | base64` " +
	"and plan the rotation (guide/operate/04-secure.md, guide/operate/12-keys.md). A later release will refuse to start"

const weakCookieSecretLog = "WARNING: AUTH=%s and AF_COOKIE_SECRET is shorter than 32 bytes of key material (or is a repeated pattern). " +
	"Session, sign-in state and preview cookies are signed with it. Generate one with `head -c 32 /dev/urandom | base64`; " +
	"changing it signs everyone out once (guide/operate/04-secure.md). A later release will refuse to start"

// weakKeyChecks evaluates both secrets for a deployment signing real people in. AUTH=dev is
// exempt: it has no authentication to protect.
func weakKeyChecks(authMode, masterKey, cookieSecret string) (weakMaster, weakCookie bool) {
	if authMode == "dev" {
		return false, false
	}
	return weakKeyMaterial(masterKey), weakKeyMaterial(cookieSecret)
}
