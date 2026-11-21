package claude

import (
	"testing"
	"time"
)

// A PTY read that ends in the middle of the URL must not be returned: the scrape waits for the
// end of the line, so the browser never gets a truncated URL.
func TestUrlReScrapesBothAuthorizeHosts(t *testing.T) {
	cases := map[string]string{
		"2.1.330 claude.ai":    "https://claude.ai/oauth/authorize?code=true&client_id=abc&response_type=code&state=xyz",
		"older claude.com/cai": "https://claude.com/cai/oauth/authorize?code=true&client_id=abc&state=xyz",
	}
	for name, url := range cases {
		t.Run(name, func(t *testing.T) {
			half := len(url) / 2
			script := "printf 'Opening browser to sign in…\\r\\nIf the browser didn'\"'\"'t open, visit:\\r\\n';" +
				"printf '\\033]8;;" + url + "\\007\\033[94m" + url[:half] + "';sleep 1;" +
				"printf '" + url[half:] + "\\033[39m\\033]8;;\\007\\r\\nPaste code here if prompted > ';sleep 30"
			f := flowOf(t, script)
			if got := f.WaitForURL(urlRe, 10*time.Second); got != url {
				t.Fatalf("WaitForURL = %q, want %q", got, url)
			}
		})
	}
}

func TestUrlReRejectsOtherHosts(t *testing.T) {
	for _, s := range []string{
		"https://evil.example/oauth/authorize?x=1",
		"https://claude.ai.evil.example/oauth/authorize?x=1",
		"https://notclaude.ai/oauth/authorize?x=1",
		"https://claude.com/oauth/authorize?x=1",
	} {
		if m := urlRe.FindString(s); m != "" {
			t.Errorf("urlRe matched %q in %q", m, s)
		}
	}
}
