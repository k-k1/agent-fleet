package localredirect

import "testing"

func TestSafe(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"console path", "/sessions", "/sessions"},
		{"tenant query", "/?tenant=sales", "/?tenant=sales"},
		{"session query", "/s/abc?session=42&tenant=dev", "/s/abc?session=42&tenant=dev"},
		{"preview app path", "/app/index.html?x=1&y=a%20b", "/app/index.html?x=1&y=a%20b"},
		{"fragment kept", "/a?b=c#d", "/a?b=c#d"},
		{"escaped segment kept", "/a%2Fb/c", "/a%2Fb/c"},
		{"empty", "", "/"},
		{"relative", "evil.example", "/"},
		{"scheme relative", "//evil.example", "/"},
		{"absolute", "https://evil.example/x", "/"},
		{"leading backslash", `/\evil.example`, "/"},
		{"backslash later", `/a\b`, "/"},
		{"encoded backslash", "/%5Cevil.example", "/"},
		{"encoded slash after slash", "/%2Fevil.example", "/"},
		{"tab", "/\tevil.example", "/"},
		{"newline", "/a\nb", "/"},
		{"nul", "/a\x00b", "/"},
		{"del", "/a\x7fb", "/"},
		{"userinfo form", "/@evil.example", "/@evil.example"},
		{"bad escape", "/a%zz", "/"},
		{"query backslash", `/a?b=\c`, "/"},
	} {
		if got := Safe(tc.in); got != tc.want {
			t.Errorf("%s: Safe(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// The value that comes out must itself survive the check unchanged.
func TestSafeIsIdempotent(t *testing.T) {
	for _, in := range []string{"/", "/a/b?c=d", "/a%2Fb?x=%2F", "/p#frag", "/%41"} {
		once := Safe(in)
		if twice := Safe(once); twice != once {
			t.Errorf("not idempotent for %q: %q then %q", in, once, twice)
		}
	}
}
