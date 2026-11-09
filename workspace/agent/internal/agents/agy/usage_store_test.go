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

// The usage payload carries the model limits learned from failed calls next to the scraped
// pools, and never one whose reset has passed.
func TestUsageCarriesLearnedLimits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	prevScrape, prevLearned := scrape, LearnedWindows
	scrape = func() (*usageResult, error) {
		return &usageResult{Groups: []UsageGroup{{Label: "GEMINI MODELS", RemainingPct: 86}}}, nil
	}
	LearnedWindows = func(time.Time) []LearnedWindow {
		return []LearnedWindow{
			{Model: "", Until: now.Add(2 * time.Hour)},
			{Model: "gemini-4-argon", Until: now.Add(100 * time.Hour)},
			{Model: "old", Until: now.Add(-time.Minute)},
		}
	}
	usageMu.Lock()
	prevCached, prevAt := usageCached, usageAt
	usageCached, usageAt = nil, time.Time{}
	usageMu.Unlock()
	t.Cleanup(func() {
		scrape, LearnedWindows = prevScrape, prevLearned
		usageMu.Lock()
		usageCached, usageAt = prevCached, prevAt
		usageMu.Unlock()
	})
	writeToken(t, "a@example.com")

	out := agyUsageJSON(t)
	l, _ := out["learned"].([]any)
	if len(l) != 2 {
		t.Fatalf("learned = %+v, want 2 live entries", out["learned"])
	}
	m := l[1].(map[string]any)
	if m["model"] != "gemini-4-argon" || m["resetsAt"] == "" {
		t.Errorf("entry = %+v", m)
	}
	if out["groups"] == nil {
		t.Error("scraped groups must stay beside the learned ones")
	}
}

// A failed scrape with nothing cached is exactly when a member needs the learned limit: the
// pools are missing, yet the model that refused calls is known.
func TestFailedScrapeStillCarriesLearnedLimits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prevScrape, prevLearned := scrape, LearnedWindows
	scrape = func() (*usageResult, error) { return nil, errors.New("tui did not answer") }
	LearnedWindows = func(now time.Time) []LearnedWindow {
		return []LearnedWindow{{Model: "gemini-4-argon", Until: now.Add(time.Hour)}}
	}
	usageMu.Lock()
	prevCached, prevAt := usageCached, usageAt
	usageCached, usageAt = nil, time.Time{}
	usageMu.Unlock()
	t.Cleanup(func() {
		scrape, LearnedWindows = prevScrape, prevLearned
		usageMu.Lock()
		usageCached, usageAt = prevCached, prevAt
		usageMu.Unlock()
	})
	writeToken(t, "a@example.com")
	out := agyUsageJSON(t)
	if l, _ := out["learned"].([]any); out["ok"] != false || len(l) != 1 {
		t.Fatalf("usage = %+v, want ok=false with one learned limit", out)
	}
}

func TestLearnedLimitsIsAnEmptyArrayNotNull(t *testing.T) {
	prev := LearnedWindows
	LearnedWindows = nil
	t.Cleanup(func() { LearnedWindows = prev })
	b, _ := json.Marshal(learnedLimits(time.Now()))
	if string(b) != "[]" {
		t.Fatalf("wire = %s, want []", b)
	}
}
