package muse

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp/msptest"
)

// resetQuota isolates a test from the process-wide observation the chip reads.
func resetQuota(t *testing.T) {
	t.Helper()
	quotaMu.Lock()
	prev := quotaSeen
	quotaSeen = nil
	quotaMu.Unlock()
	t.Cleanup(func() {
		quotaMu.Lock()
		quotaSeen = prev
		quotaMu.Unlock()
	})
}

func usageJSON(t *testing.T) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	HandleUsage(rec, httptest.NewRequest(http.MethodGet, "/muse/usage", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %s: %v", rec.Body.String(), err)
	}
	return out
}

// The unsolicited notification is the only route by which the chip learns anything without
// being asked, so it has to reach the store the endpoint reads.
func TestUsageChangedReachesTheChip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetQuota(t)
	writeAuth(t, accountJSON)
	installedMuse(t)

	h := &threadHandle{}
	host := newTestHandle(t, h)
	resetAt := time.Now().Add(2 * time.Hour).UnixMilli()
	host.Notify(msp.NotificationUsageChanged, map[string]any{
		"observedAtMs": time.Now().UnixMilli(),
		"tier":         "pro",
		"window":       map[string]any{"resetsAtMs": resetAt, "usedPercent": 37, "windowDurationMins": 300},
		"weekly":       map[string]any{"resetsAtMs": resetAt, "usedPercent": 12},
	})
	waitFor(t, func() bool { return lastQuota() != nil })

	out := usageJSON(t)
	if out["ok"] != true || out["authed"] != true {
		t.Fatalf("usage = %+v", out)
	}
	five, _ := out["fiveHour"].(map[string]any)
	if five == nil || five["pct"] != float64(37) {
		t.Errorf("window = %+v", five)
	}
	week, _ := out["sevenDay"].(map[string]any)
	if week == nil || week["pct"] != float64(12) {
		t.Errorf("weekly = %+v", week)
	}
	// The window's length is the wire's, not a constant: five hours is what this build
	// reports, not what the protocol promises.
	if out["windowMins"] != float64(300) {
		t.Errorf("windowMins = %v", out["windowMins"])
	}
	if out["plan"] != "pro" {
		t.Errorf("plan = %v", out["plan"])
	}
}

// 🔴 "Signed in with nothing observed" and "not signed in" are different answers, and the
// chip shows different things for them. Measured, `usage/read` answers {} until that host has
// seen a completion, so a workspace whose sessions have just started is in the first state —
// reporting 0% used there would tell the member they had their whole week left.
func TestUsageSaysAuthedWithNoReading(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetQuota(t)
	writeAuth(t, accountJSON)
	installedMuse(t)

	out := usageJSON(t)
	if out["ok"] != false || out["authed"] != true {
		t.Fatalf("usage = %+v, want ok=false authed=true", out)
	}
	if _, ok := out["fiveHour"]; ok {
		t.Error("a window was reported with nothing observed")
	}
}

func TestUsageSaysNotAuthedWithoutACredential(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetQuota(t)
	installedMuse(t)

	out := usageJSON(t)
	if out["authed"] != false || out["ok"] != false {
		t.Fatalf("usage = %+v, want ok=false authed=false", out)
	}
}

// Two hosts observe the same account and can deliver out of order. An older reading
// overwriting a newer one is a chip that walks backwards, which reads as usage being
// refunded.
func TestOlderObservationDoesNotOverwriteANewerOne(t *testing.T) {
	resetQuota(t)
	now := time.Now().UnixMilli()
	recordQuota(msp.SubscriptionUsage{ObservedAtMs: now, Tier: "pro",
		Window: msp.SubscriptionUsageWindow{UsedPercent: 80, WindowDurationMins: 300}})
	recordQuota(msp.SubscriptionUsage{ObservedAtMs: now - 60_000, Tier: "pro",
		Window: msp.SubscriptionUsageWindow{UsedPercent: 10, WindowDurationMins: 300}})
	if got := lastQuota(); got == nil || got.Window.UsedPercent != 80 {
		t.Fatalf("kept %+v, want the 80%% observation", got)
	}
	// The control: a newer one does replace it, or the guard above would be "never update".
	recordQuota(msp.SubscriptionUsage{ObservedAtMs: now + 60_000, Tier: "pro",
		Window: msp.SubscriptionUsageWindow{UsedPercent: 91, WindowDurationMins: 300}})
	if got := lastQuota(); got == nil || got.Window.UsedPercent != 91 {
		t.Fatalf("kept %+v, want the 91%% observation", got)
	}
}

// A reset the provider did not name must not become 1970 on the screen.
func TestNoResetStampRendersEmpty(t *testing.T) {
	if got := epochMsToRFC3339(0); got != "" {
		t.Errorf("epochMsToRFC3339(0) = %q", got)
	}
	if got := epochMsToRFC3339(1_800_000_000_000); got == "" {
		t.Error("a real stamp rendered empty")
	}
}

// usage/read is the pull half: a Console that opens the chip before any notification has
// arrived still gets the host's own last observation.
func TestUsageReadAsksALiveHost(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetQuota(t)
	writeAuth(t, accountJSON)
	installedMuse(t)

	h := &threadHandle{name: "muse-usage-read"}
	host := newTestHandle(t, h)
	handlesMu.Lock()
	handles[h.name] = h
	handlesMu.Unlock()
	t.Cleanup(func() {
		handlesMu.Lock()
		delete(handles, h.name)
		handlesMu.Unlock()
	})
	asked := make(chan msptest.Message, 1)
	host.Handle(msp.MethodUsageRead, func(m msptest.Message) (any, *msp.Error) {
		asked <- m
		return map[string]any{"usage": map[string]any{
			"observedAtMs": time.Now().UnixMilli(),
			"tier":         "max",
			"window":       map[string]any{"resetsAtMs": 0, "usedPercent": 55, "windowDurationMins": 300},
			"weekly":       map[string]any{"resetsAtMs": 0, "usedPercent": 5},
		}}, nil
	})

	out := usageJSON(t)
	select {
	case <-asked:
	default:
		t.Fatal("the live host was never asked")
	}
	if out["ok"] != true || out["plan"] != "max" {
		t.Fatalf("usage = %+v", out)
	}
	// resetsAtMs 0 means the provider named no reset, and the row must not claim 1970.
	if five, _ := out["fiveHour"].(map[string]any); five == nil || five["resetsAt"] != "" {
		t.Errorf("window = %+v", five)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !cond() {
		select {
		case <-deadline:
			t.Fatal("condition never became true")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// The reported case: weekly at 99% stops every turn, so the observation freezes with a 5-hour
// 98% whose reset has passed. The chip must get that window as stale (and rolled forward), and
// the weekly one untouched.
func TestUsageMarksAPastWindowStale(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetQuota(t)
	writeAuth(t, accountJSON)
	installedMuse(t)

	past := time.Now().Add(-time.Hour)
	weekly := time.Now().Add(4 * 24 * time.Hour)
	recordQuota(msp.SubscriptionUsage{ObservedAtMs: time.Now().Add(-2 * time.Hour).UnixMilli(), Tier: "pro",
		Window: msp.SubscriptionUsageWindow{UsedPercent: 98, WindowDurationMins: 300, ResetsAtMs: past.UnixMilli()},
		Weekly: msp.SubscriptionUsageWeekly{UsedPercent: 99, ResetsAtMs: weekly.UnixMilli()}})

	out := usageJSON(t)
	five, _ := out["fiveHour"].(map[string]any)
	if five["stale"] != true || five["pct"] != float64(0) {
		t.Fatalf("past window = %+v, want pct 0 and stale", five)
	}
	at, err := time.Parse(time.RFC3339, five["resetsAt"].(string))
	if err != nil || !at.After(time.Now()) {
		t.Errorf("resetsAt %v not rolled into the future (%v)", five["resetsAt"], err)
	}
	week, _ := out["sevenDay"].(map[string]any)
	if week["stale"] != nil || week["pct"] != float64(99) {
		t.Errorf("live weekly = %+v, want 99 and no stale", week)
	}
}

// windowMins can be 0 on the wire: then the length is unknown, so the window is flagged but
// its reset stays where the host put it.
func TestQuotaWindowWithoutLengthIsNotRolled(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Hour)
	w := quotaWindow(98, 0, past.UnixMilli(), now)
	if w["stale"] != true || w["pct"] != float64(0) {
		t.Fatalf("window = %+v", w)
	}
	if w["resetsAt"] != past.UTC().Truncate(time.Second).Format(time.RFC3339) {
		t.Errorf("resetsAt = %v, want the recorded instant", w["resetsAt"])
	}
}

// After an Agent restart no muse host is running and nothing is pushed, so the chip has only
// what usagestore kept: the last reading, with its real age (#3127).
func TestUsageSurvivesARestart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetQuota(t)
	writeAuth(t, accountJSON)
	installedMuse(t)
	observed := time.Now().Add(-90 * time.Minute)
	resetAt := time.Now().Add(2 * time.Hour).UnixMilli()
	recordQuota(msp.SubscriptionUsage{ObservedAtMs: observed.UnixMilli(), Tier: "pro",
		Window: msp.SubscriptionUsageWindow{UsedPercent: 37, WindowDurationMins: 300, ResetsAtMs: resetAt},
		Weekly: msp.SubscriptionUsageWeekly{UsedPercent: 12, ResetsAtMs: resetAt}})

	quotaMu.Lock()
	quotaSeen = nil // the restart: this process never received the push
	quotaMu.Unlock()

	out := usageJSON(t)
	if out["ok"] != true {
		t.Fatalf("usage = %+v, want the stored reading", out)
	}
	if fh, _ := out["fiveHour"].(map[string]any); fh["pct"] != float64(37) {
		t.Errorf("fiveHour = %v, want 37%%", out["fiveHour"])
	}
	if age, _ := out["ageSec"].(float64); age < 85*60 {
		t.Errorf("ageSec = %v, want the observation's age (~90 min), not the load time", out["ageSec"])
	}
	if lr := LimitReading(); len(lr.Windows) != 2 || !lr.Windows[0].ObservedAt.Equal(time.UnixMilli(observed.UnixMilli()).UTC()) {
		t.Errorf("limit reading = %+v, want both stored windows at their observation time", lr)
	}

	// Another account signs in: the previous one's reading is not this one's.
	writeAuth(t, `{"schema_version":1,"providers":{"meta":{"access_token":"dca:X","mechanism":"oauth","user_email":"other@example.com"}}}`)
	if out := usageJSON(t); out["ok"] != false {
		t.Errorf("usage after a re-login = %+v, want no reading", out)
	}
}
