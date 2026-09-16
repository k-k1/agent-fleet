package codex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// A real token_count line captured from a live codex rollout (2026-07-04).
const codexRateLimitLine = `{"timestamp":"2026-07-04T03:10:38.864Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":297059,"cached_input_tokens":226304,"output_tokens":3320,"reasoning_output_tokens":867,"total_tokens":300379},"last_token_usage":{"input_tokens":38619,"cached_input_tokens":24448,"output_tokens":540,"reasoning_output_tokens":84,"total_tokens":39159},"model_context_window":258400},"rate_limits":{"limit_id":"codex","limit_name":null,"primary":{"used_percent":3.0,"window_minutes":300,"resets_at":1783142674},"secondary":{"used_percent":0.0,"window_minutes":10080,"resets_at":1783729474},"credits":null,"individual_limit":null,"plan_type":"plus","rate_limit_reached_type":null}}}`

// Newer unified agentic-usage accounts can expose only a weekly window, in primary.
const codexWeeklyPrimaryLine = `{"timestamp":"2026-07-13T11:18:00Z","type":"event_msg","payload":{"type":"token_count","info":{"model_context_window":258400},"rate_limits":{"limit_id":"codex","primary":{"used_percent":12.0,"window_minutes":10080,"resets_at":1893456000},"secondary":null,"plan_type":"plus"}}}`

func TestCodexRateLimits(t *testing.T) {
	// Parse via the same path HandleUsage uses. The recorded %s are time-adjusted
	// (see TestCodexAdjustWindow), so here we only assert the shape parses.
	u, ok := usageFromRolloutBytes([]byte(codexRateLimitLine))
	if !ok || !u.OK {
		t.Fatalf("expected a usage reading, got ok=%v u=%+v", ok, u)
	}
	if u.FiveHour == nil || u.SevenDay == nil {
		t.Fatalf("both windows expected, got %+v", u)
	}
	if u.PlanType != "plus" {
		t.Fatalf("planType = %q, want plus", u.PlanType)
	}
	if u.FiveHour.ResetsAt == "" {
		t.Fatalf("fiveHour.ResetsAt empty")
	}
}

func TestCodexWeeklyWindowInPrimary(t *testing.T) {
	u, ok := usageFromRolloutBytes([]byte(codexWeeklyPrimaryLine))
	if !ok || !u.OK {
		t.Fatalf("expected a usage reading, got ok=%v u=%+v", ok, u)
	}
	if u.FiveHour != nil {
		t.Fatalf("fiveHour = %+v, want nil", u.FiveHour)
	}
	if u.SevenDay == nil || u.SevenDay.Pct != 12.0 {
		t.Fatalf("sevenDay = %+v, want 12%%", u.SevenDay)
	}
}

func TestCodexFullResetExpiries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" || r.Header.Get("ChatGPT-Account-Id") != "acct" {
			t.Fatal("missing ChatGPT authentication headers")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"available_count":3,"credits":[{"expires_at":"2026-07-18T00:00:00Z"},{"expires_at":null}]}`))
	}))
	defer srv.Close()

	resets, ok := getResetCredits(context.Background(), srv.Client(), srv.URL, "token", "acct")
	if !ok || resets.AvailableCount != 3 || len(resets.Credits) != 1 {
		t.Fatalf("reset credits = %+v, ok=%v", resets, ok)
	}
	if got := resets.Credits[0].ExpiresAt; got != "2026-07-18T00:00:00Z" {
		t.Fatalf("expiresAt = %q", got)
	}
}

func TestCodexWindowClassificationTolerance(t *testing.T) {
	if !isApproxWindow(304, 300) {
		t.Fatal("304 minutes should classify as an approximately 5h window")
	}
	if isApproxWindow(360, 300) {
		t.Fatal("360 minutes must not classify as an approximately 5h window")
	}
}

func TestCodexAdjustWindow(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)

	// A window still in the future passes through unchanged.
	future := now.Add(2 * time.Hour).Unix()
	if w := adjustWindow(37.0, 300, future, now); w.Pct != 37.0 {
		t.Fatalf("live window pct = %v, want 37.0", w.Pct)
	}

	// An expired window (reset in the past) zeroes the %, and rolls the reset forward
	// by whole windows to a future instant.
	past := now.Add(-7 * time.Hour).Unix() // 5h window reset 7h ago
	w := adjustWindow(3.0, 300, past, now)
	if w.Pct != 0 {
		t.Fatalf("expired window pct = %v, want 0", w.Pct)
	}
	rt, err := time.Parse(time.RFC3339, w.ResetsAt)
	if err != nil || !rt.After(now) {
		t.Fatalf("expired reset = %q (err=%v), want a future instant", w.ResetsAt, err)
	}
}

// A line with no rate_limits must not be read as a usage reading.
func TestCodexRateLimitsAbsent(t *testing.T) {
	line := `{"timestamp":"2026-07-04T03:10:38.864Z","type":"event_msg","payload":{"type":"token_count","info":{"model_context_window":258400}}}`
	if _, ok := usageFromRolloutBytes([]byte(line)); ok {
		t.Fatalf("expected no reading for a rate_limits-free line")
	}
}

func resetObservedRateLimits() {
	observedRateLimits.Lock()
	defer observedRateLimits.Unlock()
	observedRateLimits.primary, observedRateLimits.secondary = nil, nil
	observedRateLimits.planType = ""
	observedRateLimits.at = time.Time{}
}

// A fresh app-server push must beat the rollout snapshot recorded on a past turn.
func TestCodexObservedRateLimitsBeatOlderRollout(t *testing.T) {
	resetObservedRateLimits()
	t.Cleanup(resetObservedRateLimits)
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	roll := filepath.Join(dir, ".codex", "sessions", "2026", "07", "04")
	if err := os.MkdirAll(roll, 0o755); err != nil {
		t.Fatal(err)
	}
	// codexRateLimitLine's timestamp is days old; the push below is seconds old.
	if err := os.WriteFile(filepath.Join(roll, "rollout-2026-07-04T09-24-19-abc.jsonl"), []byte(codexRateLimitLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	SetObservedRateLimits(&RateLimitWindow{
		UsedPercent: 93, WindowMinutes: 10080, ResetsAt: time.Now().Add(48 * time.Hour).Unix(),
	}, nil, "plus")

	u := readUsage()
	if !u.OK || u.SevenDay == nil || u.SevenDay.Pct != 93 {
		t.Fatalf("readUsage = %+v, want the observed 93%% weekly reading", u)
	}
	if u.FiveHour != nil {
		t.Fatalf("fiveHour = %+v, want nil (weekly-only push)", u.FiveHour)
	}
	if u.PlanType != "plus" || u.AgeSec < 0 {
		t.Fatalf("planType=%q ageSec=%d, want plus with a non-negative age", u.PlanType, u.AgeSec)
	}
}

// A push observed before the newest rollout reading must lose to the rollout.
func TestCodexObservedRateLimitsStaleLosesToRollout(t *testing.T) {
	resetObservedRateLimits()
	t.Cleanup(resetObservedRateLimits)
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	roll := filepath.Join(dir, ".codex", "sessions", "2026", "07", "15")
	if err := os.MkdirAll(roll, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	line := `{"timestamp":"` + now.Format(time.RFC3339) + `","type":"event_msg","payload":{"type":"token_count","info":{},"rate_limits":{"primary":{"used_percent":3.0,"window_minutes":300,"resets_at":` +
		strconv.FormatInt(now.Add(2*time.Hour).Unix(), 10) + `},"secondary":null,"plan_type":"plus"}}}`
	if err := os.WriteFile(filepath.Join(roll, "rollout-2026-07-15T00-00-00-abc.jsonl"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	SetObservedRateLimits(&RateLimitWindow{
		UsedPercent: 93, WindowMinutes: 10080, ResetsAt: now.Add(48 * time.Hour).Unix(),
	}, nil, "plus")
	observedRateLimits.Lock()
	observedRateLimits.at = time.Now().Add(-time.Hour)
	observedRateLimits.Unlock()

	u := readUsage()
	if !u.OK || u.FiveHour == nil || u.FiveHour.Pct != 3.0 {
		t.Fatalf("readUsage = %+v, want the rollout 3%% five-hour reading", u)
	}
}

// With no rollout reading at all, even an old push is better than nothing.
func TestCodexObservedRateLimitsUsedWithoutRollout(t *testing.T) {
	resetObservedRateLimits()
	t.Cleanup(resetObservedRateLimits)
	t.Setenv("HOME", t.TempDir())
	SetObservedRateLimits(&RateLimitWindow{
		UsedPercent: 42, WindowMinutes: 300, ResetsAt: time.Now().Add(time.Hour).Unix(),
	}, nil, "plus")
	u := readUsage()
	if !u.OK || u.FiveHour == nil || u.FiveHour.Pct != 42 {
		t.Fatalf("readUsage = %+v, want the observed 42%% five-hour reading", u)
	}
}

// readUsage picks the freshest reading from the newest rollout on disk.
func TestReadCodexUsageFromDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	roll := filepath.Join(dir, ".codex", "sessions", "2026", "07", "04")
	if err := os.MkdirAll(roll, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roll, "rollout-2026-07-04T09-24-19-abc.jsonl"), []byte(codexRateLimitLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	u := readUsage()
	if !u.OK || u.FiveHour == nil || u.SevenDay == nil {
		t.Fatalf("readUsage = %+v, want ok with both windows", u)
	}
}

// account/rateLimits/updated is a sparse rolling update: a push carrying only primary must
// not erase the weekly window or planType observed just before it (a missing field means
// "unchanged", not "gone").
func TestCodexObservedRateLimitsSparseMerge(t *testing.T) {
	resetObservedRateLimits()
	t.Cleanup(resetObservedRateLimits)
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	SetObservedRateLimits(
		&RateLimitWindow{UsedPercent: 10, WindowMinutes: 300, ResetsAt: now.Add(time.Hour).Unix()},
		&RateLimitWindow{UsedPercent: 40, WindowMinutes: 10080, ResetsAt: now.Add(48 * time.Hour).Unix()},
		"plus")
	// Sparse push carrying only the 5h window (no secondary, no planType = unchanged).
	SetObservedRateLimits(&RateLimitWindow{
		UsedPercent: 12, WindowMinutes: 300, ResetsAt: now.Add(time.Hour).Unix(),
	}, nil, "")

	u := readUsage()
	if !u.OK || u.FiveHour == nil || u.FiveHour.Pct != 12 {
		t.Fatalf("readUsage = %+v, want the updated 12%% five-hour window", u)
	}
	if u.SevenDay == nil || u.SevenDay.Pct != 40 {
		t.Fatalf("sevenDay = %+v, want the retained 40%% weekly window", u.SevenDay)
	}
	if u.PlanType != "plus" {
		t.Fatalf("planType = %q, want the retained \"plus\"", u.PlanType)
	}
}

// ResetAt (docs/log/47 §4-12) names the instant the usage limit that stopped a turn lifts.
// What it has to get right is WHICH window it takes when more than one could apply, and that
// it stays silent rather than guessing — the caller books a wake-up on this answer.
func TestCodexResetAtPicksEarliestFullWindow(t *testing.T) {
	resetObservedRateLimits()
	t.Cleanup(resetObservedRateLimits)
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	fiveHourReset := now.Add(time.Hour).Truncate(time.Second)
	// Both windows are full: the 5-hour one inside an exhausted week.
	SetObservedRateLimits(
		&RateLimitWindow{UsedPercent: 100, WindowMinutes: 300, ResetsAt: fiveHourReset.Unix()},
		&RateLimitWindow{UsedPercent: 100, WindowMinutes: 10080, ResetsAt: now.Add(48 * time.Hour).Unix()},
		"plus")

	at, source, ok := ResetAt(now)
	if !ok {
		t.Fatalf("ResetAt = !ok, want the 5-hour reset (nothing would be booked)")
	}
	if !at.Equal(fiveHourReset.UTC()) || source != "codex:5h" {
		t.Errorf("ResetAt = %v / %q, want %v / codex:5h — a session parked on the weekly reset "+
			"sleeps for days while its window reopens in an hour", at, source, fiveHourReset.UTC())
	}
}

// A window that is not full is not the one that stopped the turn, however soon it resets.
func TestCodexResetAtSkipsWindowsThatAreNotFull(t *testing.T) {
	resetObservedRateLimits()
	t.Cleanup(resetObservedRateLimits)
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	weeklyReset := now.Add(48 * time.Hour).Truncate(time.Second)
	SetObservedRateLimits(
		&RateLimitWindow{UsedPercent: 40, WindowMinutes: 300, ResetsAt: now.Add(time.Hour).Unix()},
		&RateLimitWindow{UsedPercent: 100, WindowMinutes: 10080, ResetsAt: weeklyReset.Unix()},
		"plus")

	at, source, ok := ResetAt(now)
	if !ok || !at.Equal(weeklyReset.UTC()) || source != "codex:weekly" {
		t.Errorf("ResetAt = %v / %q / ok=%v, want %v / codex:weekly — resuming at the 5-hour "+
			"reset walks straight back into the weekly limit", at, source, ok, weeklyReset.UTC())
	}
}

// Nothing full: no instant can be named. Silence is the answer, not the nearest reset.
func TestCodexResetAtWithoutAFullWindow(t *testing.T) {
	resetObservedRateLimits()
	t.Cleanup(resetObservedRateLimits)
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	SetObservedRateLimits(
		&RateLimitWindow{UsedPercent: 40, WindowMinutes: 300, ResetsAt: now.Add(time.Hour).Unix()},
		&RateLimitWindow{UsedPercent: 12, WindowMinutes: 10080, ResetsAt: now.Add(48 * time.Hour).Unix()},
		"plus")

	if at, source, ok := ResetAt(now); ok {
		t.Errorf("ResetAt = %v / %q / ok=true, want !ok", at, source)
	}
}

// A reading whose window has already reset says nothing either: adjustWindow zeroes its
// percentage, so a stale snapshot cannot be mistaken for a limit in force. This is the
// property that keeps an old rollout from booking a resume at an instant in the past.
func TestCodexResetAtIgnoresAReadingThatHasAlreadyReset(t *testing.T) {
	resetObservedRateLimits()
	t.Cleanup(resetObservedRateLimits)
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	SetObservedRateLimits(&RateLimitWindow{
		UsedPercent: 100, WindowMinutes: 300, ResetsAt: now.Add(-2 * time.Hour).Unix(),
	}, nil, "plus")

	if at, source, ok := ResetAt(now); ok {
		t.Errorf("ResetAt = %v / %q / ok=true, want !ok (a spent window read as a live limit)", at, source)
	}
}

// No reading at all (a codex that has never recorded one) — the same silence.
func TestCodexResetAtWithoutAnyReading(t *testing.T) {
	resetObservedRateLimits()
	t.Cleanup(resetObservedRateLimits)
	t.Setenv("HOME", t.TempDir())
	if _, _, ok := ResetAt(time.Now()); ok {
		t.Error("ResetAt = ok with no reading available, want !ok")
	}
}

// primeAccountUsage fills the account view's cache (and a login for it to belong to) so the
// lookup answers without a network call — the same shortcut accountUsage takes on a warm cache.
func primeAccountUsage(t *testing.T, u usage) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	auth := `{"auth_mode":"chatgpt","tokens":{"access_token":"t","account_id":"a"}}`
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(auth), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &accountUsageCache
	c.Lock()
	c.val, c.ok, c.fetched = u, true, time.Now()
	c.Unlock()
	t.Cleanup(func() {
		c.Lock()
		c.val, c.ok, c.fetched = usage{}, false, time.Time{}
		c.Unlock()
	})
}

// TestCodexResetAtUsesTheAccountViewAcrossABoundary (docs/log/47 §4-13, measured on scpigmc):
// the local reading is the one written before the window boundary, so adjustWindow has zeroed
// it — and it is the FRESHEST of the two. The account view still says the plan is out until the
// next boundary, and that has to be the answer: with the local reading in charge nothing is
// ever booked again, which is exactly what left a session waiting with no reservation.
func TestCodexResetAtUsesTheAccountViewAcrossABoundary(t *testing.T) {
	resetObservedRateLimits()
	t.Cleanup(resetObservedRateLimits)
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	realReset := now.Add(4 * time.Hour).Truncate(time.Second)
	// Local: recorded before the boundary that has just passed => pct 0, reset rolled forward.
	SetObservedRateLimits(&RateLimitWindow{
		UsedPercent: 100, WindowMinutes: 300, ResetsAt: now.Add(-time.Minute).Unix(),
	}, nil, "plus")
	primeAccountUsage(t, usage{
		OK: true, AgeSec: 200,
		FiveHour: &usageWindow{Pct: 100, ResetsAt: realReset.Format(time.RFC3339)},
		SevenDay: &usageWindow{Pct: 47, ResetsAt: now.Add(6 * 24 * time.Hour).Format(time.RFC3339)},
	})

	at, source, ok := ResetAt(now)
	if !ok {
		t.Fatal("ResetAt = !ok — the account view knows the plan is out, so nothing would ever be booked")
	}
	if !at.Equal(realReset) || source != "codex:5h" {
		t.Errorf("ResetAt = %v / %q, want %v / codex:5h", at, source, realReset)
	}
}

// The local reading is still trusted on its own: it is the only one a workspace without a
// ChatGPT login (an API-key codex) has, and it is the fresher source while a session is
// working. A full window from either reading is evidence.
func TestCodexResetAtTakesAFullWindowFromEitherReading(t *testing.T) {
	resetObservedRateLimits()
	t.Cleanup(resetObservedRateLimits)
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	localReset := now.Add(time.Hour).Truncate(time.Second)
	SetObservedRateLimits(&RateLimitWindow{
		UsedPercent: 100, WindowMinutes: 300, ResetsAt: localReset.Unix(),
	}, nil, "plus")
	// The account view is behind by a window and says nothing useful (all zeroed).
	primeAccountUsage(t, usage{OK: true, AgeSec: 400})

	at, source, ok := ResetAt(now)
	if !ok || !at.Equal(localReset.UTC()) || source != "codex:5h" {
		t.Errorf("ResetAt = %v / %q / ok=%v, want %v / codex:5h", at, source, ok, localReset.UTC())
	}
}
