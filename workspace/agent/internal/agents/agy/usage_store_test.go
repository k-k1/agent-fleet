package agy

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagestore"
)

func writeToken(t *testing.T, email string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(tokenPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath(), []byte(`{"token":"x","id_token_email":"`+email+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func agyUsageJSON(t *testing.T) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	HandleUsage(rec, httptest.NewRequest(http.MethodGet, "/connections/agy/usage", nil))
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Right after an Agent restart there is no cache; when the first scrape fails the card and chip
// show the reading stored before the restart, for the same Google account only (#3127).
func TestFailedFirstScrapeServesTheStoredReading(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prevScrape := scrape
	scrape = func() (*usageResult, error) { return nil, errors.New("tui did not answer") }
	usageMu.Lock()
	prevCached, prevAt := usageCached, usageAt
	usageCached, usageAt = nil, time.Time{}
	usageMu.Unlock()
	t.Cleanup(func() {
		scrape = prevScrape
		usageMu.Lock()
		usageCached, usageAt = prevCached, prevAt
		usageMu.Unlock()
	})
	writeToken(t, "a@example.com")
	usagestore.Record("agy", "a@example.com", time.Now().Add(-time.Hour), &usageResult{
		Plan:   "Pro",
		Groups: []UsageGroup{{Label: "Gemini", RemainingPct: 70, ResetsAt: time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)}},
	})

	out := agyUsageJSON(t)
	if out["ok"] != true || out["plan"] != "Pro" {
		t.Fatalf("usage = %+v, want the stored reading", out)
	}
	if age, _ := out["ageSec"].(float64); age < 3500 {
		t.Errorf("ageSec = %v, want the stored observation's age", out["ageSec"])
	}

	usageMu.Lock()
	usageCached, usageAt = nil, time.Time{}
	usageMu.Unlock()
	writeToken(t, "b@example.com")
	if out := agyUsageJSON(t); out["ok"] != false {
		t.Errorf("usage after signing in as another account = %+v, want no reading", out)
	}
}
