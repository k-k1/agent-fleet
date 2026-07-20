package agy

import (
	"os"
	"testing"
)

// Live /context scrape against the real agy TUI, resuming the newest existing
// conversation (needs an installed, signed-in agy with at least one
// conversation). Opt-in: AF_AGY_LIVE=1 go test ./internal/agents/agy/ -run Live -v
func TestScrapeContextLive(t *testing.T) {
	if os.Getenv("AF_AGY_LIVE") == "" {
		t.Skip("set AF_AGY_LIVE=1 to run the live agy /context scrape")
	}
	if !SignedIn() {
		t.Skip("agy is not signed in")
	}
	convs := listBrainDirs()
	if len(convs) == 0 {
		t.Skip("no agy conversations to resume")
	}
	// Newest by transcript mtime so the scrape resumes a conversation that
	// actually has steps (brain dirs are created on the first prompt).
	var conv string
	var newest int64
	for _, c := range convs {
		if fi, err := os.Stat(transcriptPath(c)); err == nil && fi.ModTime().UnixNano() > newest {
			conv, newest = c, fi.ModTime().UnixNano()
		}
	}
	if conv == "" {
		t.Skip("no conversation with a transcript found")
	}
	c, err := scrapeContext(conv)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("conv=%s tokens=%d window=%d at=%s", conv, c.Tokens, c.Window, c.At)
	if c.Window <= 0 {
		t.Errorf("window = %d, want > 0", c.Window)
	}
}
