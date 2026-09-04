package agy

import (
	"strings"
	"testing"
)

// The TUI renders the authorize URL as an OSC-8 hyperlink: after ANSI
// stripping the buffer holds the URL twice back-to-back plus a "]8;;"
// remnant (measured in the integrated E2E). sanitizeAuthURL must return exactly one clean copy.
func TestSanitizeAuthURL(t *testing.T) {
	clean := "https://accounts.google.com/o/oauth2/auth?client_id=x&state=4zIlJff6QT"
	for name, in := range map[string]string{
		"already_clean":    clean,
		"doubled":          clean + clean,
		"doubled_with_osc": clean + clean + "]8;;",
		"single_with_osc":  clean + "]8;;",
	} {
		if got := sanitizeAuthURL(in); got != clean {
			t.Errorf("%s: got %q", name, got)
		}
	}
	if got := sanitizeAuthURL(""); got != "" {
		t.Errorf("empty: got %q", got)
	}
}

func TestSanitizeAuthURLKeepsFirstOfMany(t *testing.T) {
	clean := "https://accounts.google.com/o/oauth2/auth?state=abc"
	in := clean + clean + clean
	if got := sanitizeAuthURL(in); got != clean {
		t.Errorf("tripled: got %q", got)
	}
	if strings.Count(sanitizeAuthURL(in), "https://") != 1 {
		t.Error("sanitized URL must contain the scheme exactly once")
	}
}
