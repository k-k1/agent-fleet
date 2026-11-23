// Package secretpattest builds made-up GitHub tokens for the tests of every detector and
// redactor that uses secretpat. The values are assembled at run time from a fixed seed, never
// written out as literals: a literal token-shaped value trips the repository's secret scan,
// and a fixed seed keeps every run, and every package, on the same values.
//
// Imported from _test.go files only. control-plane/internal/secretpat/secretpattest and
// workspace/agent/internal/secretpat/secretpattest are byte-identical copies.
package secretpattest

import (
	"encoding/base64"
	"math/rand/v2"
	"strings"
)

func rng(tag byte) *rand.Rand {
	return rand.New(rand.NewChaCha8([32]byte{'a', 'f', '-', '4', '9', '2', '9', '-', tag}))
}

func randomBytes(r *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.UintN(256))
	}
	return b
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func prefix(kind string) string { return "gh" + kind + "_" }

// JWT is a made-up RS256 JWT: the common 36-character header, a payload and a 2048-bit
// signature, about 520 characters in all.
func JWT(tag byte) string { return jwt(tag, `{"alg":"RS256","typ":"JWT"}`) }

func jwt(tag byte, header string) string {
	r := rng(tag)
	return b64([]byte(header)) + "." + b64(randomBytes(r, 105)) + "." + b64(randomBytes(r, 256))
}

// StatelessSpacedHeader is StatelessApp whose JOSE header starts with a space, as RFC 7519
// allows: its base64url does not start with `eyJ`.
func StatelessSpacedHeader() string {
	return prefix("s") + "2873461_" + jwt('w', ` {"alg":"RS256","typ":"JWT"}`)
}

// JWTHolding is a JWT whose signature has a GitHub-token-shaped run inside it, cut short by
// `_` so a word-bounded classic pattern does not take it alone.
func JWTHolding(tok string) string { return JWT('h') + "opaquePart-" + tok + "_private" }

// StatelessApp is a stateless installation token as GitHub documents it, `ghs_APPID_JWT`.
func StatelessApp(appID string) string { return prefix("s") + appID + "_" + JWT('s') }

// Stateless is StatelessApp with an ordinary APPID.
func Stateless() string { return StatelessApp("2873461") }

// StatelessZeros has an APPID with a run of zeros, which a substring placeholder filter
// ("0000") would take for an example.
func StatelessZeros() string { return StatelessApp("100000") }

// StatelessNoApp is the shape without an APPID: its first 36 body characters are the JWT
// header, all alphanumeric, which is all an alphanumeric-only pattern takes.
func StatelessNoApp() string { return prefix("s") + JWT('n') }

// StatelessEarlyPunct has a `-` and a `_` among the body's first 16 characters, where an
// alphanumeric-only pattern with any minimum of 16 or more finds nothing.
func StatelessEarlyPunct() string {
	r := rng('p')
	return prefix("s") + "eyJ0-" + b64(randomBytes(r, 3)) + "_" + b64(randomBytes(r, 24)) + "." + b64(randomBytes(r, 105)) + "." + b64(randomBytes(r, 256))
}

// Classic is a classic personal access token's shape: `ghp_` and 36 alphanumerics.
func Classic() string {
	return prefix("p") + alnum(rng('c'), "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789")
}

// ClassicLetters is Classic without a digit, which an entropy heuristic that wants both
// letters and digits does not take.
func ClassicLetters() string {
	return prefix("p") + alnum(rng('l'), "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz")
}

func alnum(r *rand.Rand, set string) string {
	var b strings.Builder
	for range 36 {
		b.WriteByte(set[r.IntN(len(set))])
	}
	return b.String()
}

// All names every token shape, for tests that must hold for each.
func All() map[string]string {
	return map[string]string{
		"classic":               Classic(),
		"classic-letters":       ClassicLetters(),
		"stateless":             Stateless(),
		"stateless-zeros":       StatelessZeros(),
		"stateless-no-app":      StatelessNoApp(),
		"stateless-early-punct": StatelessEarlyPunct(),
		"stateless-spaced":      StatelessSpacedHeader(),
	}
}

// Leftover names the first piece of tok still in out, or "" when none is: every piece between
// `.` and `_` of at least 4 characters (the APPID included), and every 8-character window of
// the body, so a redaction that left a short fragment is caught too.
func Leftover(out, tok string) string {
	for _, s := range strings.FieldsFunc(tok, func(r rune) bool { return r == '.' || r == '_' }) {
		if len(s) >= 4 && strings.Contains(out, s) {
			return s
		}
	}
	for i := len(prefix("s")); i+8 <= len(tok); i++ {
		if w := tok[i : i+8]; strings.Contains(out, w) {
			return w
		}
	}
	return ""
}

// Benign is ordinary text with underscores, dots and hyphens that no GitHub token pattern may
// match.
var Benign = []string{
	"see ghost_writer.config.example and high_score.v2-final for details",
	"the gh_ prefix alone, ghs_ alone, ghp_short.x and ghs_a.b.c are not tokens",
	"snake_case_identifier_with_many_parts.and.dots-and-hyphens_v1.2.3",
	"run `gh pr view` then open docs/user_guide.md and release-notes_2026.11.txt",
	"open docs/ghs_status_dashboard_for_enterprise_repositories.md and ghp_release_notes_for_the_2026_cycle.txt",
	"ghs_12_eyJ.short.x and gho_9_eyJhbGciOiJ.too.short are not tokens either",
	// Split so the literal does not trip the secret scan's own 36-alphanumeric ghs_ rule.
	"docs/gh" + "s_statusdashboardforenterpriserepositories_enabled.md",
	"ghs_1_short.config.yaml and ghp_2_release_notes.v2.md stay names",
}
