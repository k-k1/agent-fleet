package agents

import (
	"regexp"
	"testing"
)

// claude 2.1.x prints the authorize URL as an OSC 8 hyperlink; the scrape must
// see exactly one URL, not the link target glued to its visible text.
func TestAnsiReStripsOSC8Hyperlink(t *testing.T) {
	const url = "https://claude.com/cai/oauth/authorize?code=true&client_id=x&state=abc"
	raw := "Browser didn't open? Use the url below:\r\n\x1b[2m\x1b]8;;" + url + "\x07" + url + "\x1b]8;;\x07\x1b[22m\r\n" +
		"\x1b]8;;" + url + "\x1b\\" + url + "\x1b]8;;\x1b\\\r\n"
	got := AnsiRe.ReplaceAllString(raw, "")
	want := "Browser didn't open? Use the url below:\r\n" + url + "\r\n" + url + "\r\n"
	if got != want {
		t.Fatalf("cleaned = %q, want %q", got, want)
	}
	m := regexp.MustCompile(`https://claude\.com/cai/oauth/authorize\?\S+`).FindString(got)
	if m != url {
		t.Fatalf("scraped %q, want %q", m, url)
	}
}
