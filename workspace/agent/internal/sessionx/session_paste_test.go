package sessionx

import "testing"

func TestSanitizeUploadName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"server.log", "server.log"},
		{"仕様 書 v2.pdf", "v2.pdf"},                      // non-ASCII collapses, leading dash trimmed → keeps ext
		{"../../etc/passwd", "passwd"},                 // traversal reduced to the base
		{`C:\Users\me\notes.txt`, "notes.txt"},         // windows path separators
		{"", "file.bin"},                               // nothing usable
		{"....", "file.bin"},                           // dots only
		{"a b\tc.md", "a-b-c.md"},                      // whitespace runs collapse
		{"UPPER_case-9.tar.gz", "UPPER_case-9.tar.gz"}, // safe chars pass through
	}
	for _, c := range cases {
		if got := sanitizeUploadName(c.in); got != c.want {
			t.Errorf("sanitizeUploadName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// Long names keep the tail (the extension matters most) and stay ≤48 runes.
	long := sanitizeUploadName("very-long-prefix-that-goes-on-and-on-and-on-forever-final.report.md")
	if len([]rune(long)) > 48 || long[len(long)-3:] != ".md" {
		t.Errorf("long name = %q, want ≤48 runes ending in .md", long)
	}
}
