package keycheck

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// All values below are obviously fake.
func TestWeak(t *testing.T) {
	b32 := make([]byte, 32)
	for i := range b32 {
		b32[i] = byte(i*7 + 1)
	}
	b31 := b32[:31]
	for _, c := range []struct {
		name string
		in   string
		weak bool
	}{
		{"unset is not weak", "", false},
		{"whitespace only is weak", "   \n", true},
		{"short passphrase", "correct horse", true},
		{"43-char passphrase of one letter", strings.Repeat("a", 60), true},
		{"repeated placeholder", strings.Repeat("changeme", 8), true},
		{"hex 32 bytes", hex.EncodeToString(b32), false},
		{"hex 16 bytes", hex.EncodeToString(b32[:16]), true},
		{"base64 32 bytes", base64.StdEncoding.EncodeToString(b32), false},
		{"base64 raw-url 32 bytes", base64.RawURLEncoding.EncodeToString(b32), false},
		{"base64 31 bytes", base64.StdEncoding.EncodeToString(b31), true},
		{"32 zero bytes in base64", base64.StdEncoding.EncodeToString(make([]byte, 32)), true},
		{"long raw string with spaces", "fake passphrase with many different words in it, long enough", false},
		{"raw string one short of 43", "fake passphrase, 42 characters long!!!!!!!", true},
		{"surrounding whitespace ignored", "  " + hex.EncodeToString(b32) + "\n", false},
	} {
		if got := Weak(c.in); got != c.weak {
			t.Errorf("%s: weakKeyMaterial = %v, want %v", c.name, got, c.weak)
		}
	}
}
