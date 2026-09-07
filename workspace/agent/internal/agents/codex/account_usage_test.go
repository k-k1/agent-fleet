package codex

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The payload the account view really returns, trimmed to what is read (measured 2026-09-07
// against this container's own login).
//
// Both reset_at are relative to NOW, not the epochs that were measured. adjustWindow zeroes a
// window whose reset instant has passed (a reading whose window has since rolled over is not
// 41% any more — it is 0%), so the captured epochs made this a time bomb: it passed for the
// few minutes between the capture and reset_at, and failed on every branch afterwards, with a
// diff that reads like the classifier broke. What the test is about is the mapping of the two
// windows, so what matters is only that both are still open.
const accountUsageFmt = `{"user_id":"user-x","account_id":"a1","email":"u@example.com",
 "plan_type":"plus",
 "rate_limit":{"allowed":true,"limit_reached":false,
  "primary_window":{"used_percent":3,"limit_window_seconds":18000,"reset_after_seconds":17979,"reset_at":%d},
  "secondary_window":{"used_percent":41,"limit_window_seconds":604800,"reset_after_seconds":5048,"reset_at":%d}},
 "credits":{"has_credits":true,"balance":"466.09"},
 "rate_limit_reset_credits":{"available_count":3}}`

func accountUsageBody() string {
	now := time.Now().Unix()
	return fmt.Sprintf(accountUsageFmt, now+17979, now+5048)
}

func TestGetAccountUsageMapsBothWindows(t *testing.T) {
	var gotAuth, gotAccount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotAccount = r.Header.Get("Authorization"), r.Header.Get("ChatGPT-Account-Id")
		_, _ = w.Write([]byte(accountUsageBody()))
	}))
	defer srv.Close()

	u, ok := getAccountUsage(context.Background(), srv.Client(), srv.URL, "token", "acct")
	if !ok || !u.OK {
		t.Fatalf("usage = %+v ok=%v", u, ok)
	}
	if gotAuth != "Bearer token" || gotAccount != "acct" {
		t.Fatalf("auth headers = %q / %q", gotAuth, gotAccount)
	}
	if u.PlanType != "plus" {
		t.Fatalf("planType = %q", u.PlanType)
	}
	// The endpoint gives SECONDS; the window classifier works in minutes. Getting that
	// conversion wrong would file the weekly window as the 5-hour one.
	if u.FiveHour == nil || u.FiveHour.Pct != 3 {
		t.Fatalf("fiveHour = %+v, want 3%%", u.FiveHour)
	}
	if u.SevenDay == nil || u.SevenDay.Pct != 41 {
		t.Fatalf("sevenDay = %+v, want 41%%", u.SevenDay)
	}
}

// A dead or changed endpoint must leave the chip exactly as it was, never empty it: this is
// an unofficial backend and it is one source of three.
func TestGetAccountUsageFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
	}{
		{"not found", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }},
		{"not json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) }},
		{"no windows", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"rate_limit":{}}`)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.h)
			defer srv.Close()
			if u, ok := getAccountUsage(context.Background(), srv.Client(), srv.URL, "t", ""); ok || u.OK {
				t.Fatalf("usage = %+v ok=%v, want no reading", u, ok)
			}
		})
	}
}

// The freshest reading wins. The whole point of adding the account view is the case where the
// local one is old — an image generation runs `--ephemeral` and writes no rollout at all, so
// without this the chip would sit on the last interactive session's numbers indefinitely.
func TestReadUsagePrefersTheFreshestReading(t *testing.T) {
	win := func(pct float64) *usageWindow { return &usageWindow{Pct: pct} }
	localStale := usage{OK: true, AgeSec: 3600, FiveHour: win(10)}
	localFresh := usage{OK: true, AgeSec: 30, FiveHour: win(20)}
	acctStale := usage{OK: true, AgeSec: 3600, FiveHour: win(70)}
	acctFresh := usage{OK: true, AgeSec: 30, FiveHour: win(80)}
	for _, tc := range []struct {
		name        string
		local, acct usage
		acctOK      bool
		wantPct     float64
	}{
		// The case this whole source exists for: an image generation writes no rollout, so
		// the local reading ages without bound while the quota is really being spent.
		{name: "account is fresher", local: localStale, acct: acctFresh, acctOK: true, wantPct: 80},
		{name: "local is fresher", local: localFresh, acct: acctStale, acctOK: true, wantPct: 20},
		{name: "no local reading at all", local: usage{AgeSec: -1}, acct: acctFresh, acctOK: true, wantPct: 80},
		{name: "account unavailable", local: localStale, acctOK: false, wantPct: 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pickUsage(tc.local, tc.acct, tc.acctOK)
			if got.FiveHour == nil || got.FiveHour.Pct != tc.wantPct {
				t.Fatalf("picked %+v, want the %v%% reading", got.FiveHour, tc.wantPct)
			}
		})
	}
}

// The cache is what keeps a chip that polls every few seconds off an unofficial endpoint.
func TestAccountUsageCacheStampsAge(t *testing.T) {
	c := &accountUsageCacheT{val: usage{OK: true}, ok: true, fetched: time.Now().Add(-42 * time.Second)}
	if got := c.aged().AgeSec; got < 41 || got > 44 {
		t.Fatalf("ageSec = %d, want ~42", got)
	}
	// A cache that never got a reading must not claim age 0 (i.e. "just measured").
	empty := &accountUsageCacheT{fetched: time.Now()}
	if got := empty.aged(); got.OK {
		t.Fatalf("empty cache reported a reading: %+v", got)
	}
}
