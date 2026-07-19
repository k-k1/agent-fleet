package agy

import (
	"os"
	"testing"
)

// Live scrape against the real agy TUI (needs an installed, signed-in agy and a
// PTY-capable host). Opt-in: AF_AGY_LIVE=1 go test ./internal/agents/agy/ -run Live -v
func TestScrapeUsageLive(t *testing.T) {
	if os.Getenv("AF_AGY_LIVE") == "" {
		t.Skip("set AF_AGY_LIVE=1 to run the live agy /usage scrape")
	}
	if !SignedIn() {
		t.Skip("agy is not signed in")
	}
	res, err := scrapeUsage()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("account=%q plan=%q", res.Account, res.Plan)
	for _, g := range res.Groups {
		t.Logf("group=%q models=%q weekly=%.2f%% resetsAt=%s", g.Label, g.Models, g.RemainingPct, g.ResetsAt)
		if g.FiveHour != nil {
			t.Logf("  fiveHour=%.2f%% resetsAt=%s", g.FiveHour.RemainingPct, g.FiveHour.ResetsAt)
		}
	}
	if len(res.Groups) < 2 {
		t.Errorf("expected 2 quota groups, got %d", len(res.Groups))
	}
}
