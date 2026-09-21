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
