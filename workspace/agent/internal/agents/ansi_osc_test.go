package agents

import (
	"regexp"
	"testing"
	"time"
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

// feed appends chunks to a bare Flow's output with a pause between them, the way drain
// does when a read boundary falls inside the URL line.
func feed(f *Flow, gap time.Duration, chunks ...string) {
	for i, c := range chunks {
		if i > 0 {
			time.Sleep(gap)
		}
		f.mu.Lock()
		f.out.WriteString(c)
		f.mu.Unlock()
	}
}

// A read that ends mid-URL or mid closing OSC must not be scraped: the first case would
// hand the browser a truncated state, the second the "]8;;" remnant this PR removes.
func TestWaitForURLWaitsForTheEndOfTheLine(t *testing.T) {
	const url = "https://accounts.google.com/o/oauth2/auth?client_id=x&state=abcdef"
	re := regexp.MustCompile(`https://accounts\.google\.com/o/oauth2/auth\?\S+`)
	open := "\x1b]8;;" + url + "\x07"
	closeOSC := "\x1b]8;;\x07"
	cases := map[string][2]string{
		"split inside the visible URL": {open + url[:len(url)-3], url[len(url)-3:] + closeOSC + "\r\n"},
		"split inside the closing OSC": {open + url + "\x1b]8;;", "\x07\r\n"},
		"split before the line feed":   {open + url + closeOSC, "\r\nPaste code here > "},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := &Flow{endedCh: make(chan struct{})}
			go feed(f, 500*time.Millisecond, c[0], c[1])
			if got := f.WaitForURL(re, 5*time.Second); got != url {
				t.Fatalf("WaitForURL = %q, want %q", got, url)
			}
		})
	}
}

// Once the output has ended nothing more can follow the URL, so it is returned as is.
func TestWaitForURLAcceptsAMatchAtEOF(t *testing.T) {
	const url = "https://claude.com/cai/oauth/authorize?state=abc"
	f := &Flow{endedCh: make(chan struct{}), ended: true}
	f.out.WriteString("open " + url)
	start := time.Now()
	if got := f.WaitForURL(regexp.MustCompile(`https://claude\.com/cai/oauth/authorize\?\S+`), 5*time.Second); got != url {
		t.Fatalf("WaitForURL = %q, want %q", got, url)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("waited %v on ended output", d)
	}
}
