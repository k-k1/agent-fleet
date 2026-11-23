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

func randomBytes(r *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.UintN(256))
	}
	return b
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func prefix(kind string) string { return "gh" + kind + "_" }

// Stateless is a stateless installation token's shape: `ghs_`, a JWT whose header is the
// common 36-character `{"alg":"RS256","typ":"JWT"}`, a payload and a signature, about 520
// characters in all. An alphanumeric-only pattern matches its header and stops at the first `.`.
func Stateless() string {
	r := rand.New(rand.NewChaCha8([32]byte{'a', 'f', '-', '4', '9', '2', '9', '-', 'h'}))
	return prefix("s") + b64([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + b64(randomBytes(r, 285)) + "." + b64(randomBytes(r, 64))
}

// StatelessEarlyPunct is the same shape with a `-` and a `_` among the body's first 16
// characters, where an alphanumeric-only pattern with any minimum of 16 or more finds nothing.
func StatelessEarlyPunct() string {
	r := rand.New(rand.NewChaCha8([32]byte{'a', 'f', '-', '4', '9', '2', '9', '-', 'p'}))
	return prefix("s") + "eyJ0-" + b64(randomBytes(r, 3)) + "_" + b64(randomBytes(r, 24)) + "." + b64(randomBytes(r, 300)) + "." + b64(randomBytes(r, 64))
}

// Classic is a classic personal access token's shape: `ghp_` and 36 alphanumerics.
func Classic() string {
	const alnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	r := rand.New(rand.NewChaCha8([32]byte{'a', 'f', '-', '4', '9', '2', '9', '-', 'c'}))
	var b strings.Builder
	for range 36 {
		b.WriteByte(alnum[r.IntN(len(alnum))])
	}
	return prefix("p") + b.String()
}

// Segments are the parts of tok between dots, the pieces a partial redaction leaves behind.
func Segments(tok string) []string { return strings.Split(tok, ".") }

// Leftover names the first piece of tok, at least 6 characters long, that is still in out, or
// "" when none is. Every dot-separated segment is checked, and so is every 12-character window
// of the body, so a redaction that ate a segment's head but not its tail is caught too.
func Leftover(out, tok string) string {
	for _, s := range Segments(tok) {
		if len(s) >= 6 && strings.Contains(out, s) {
			return s
		}
	}
	for i := len(prefix("s")); i+12 <= len(tok); i++ {
		if w := tok[i : i+12]; strings.Contains(out, w) {
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
}
