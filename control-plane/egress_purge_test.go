package main

import (
	"context"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestParseEgressRetention(t *testing.T) {
	day := 24 * time.Hour
	for in, want := range map[string]time.Duration{
		"": 30 * day, "7": 7 * day, "0": 0, "-1": 30 * day, "abc": 30 * day, "1.5": 30 * day,
	} {
		if got := parseEgressRetention(in); got != want {
			t.Errorf("parseEgressRetention(%q) = %s, want %s", in, got, want)
		}
	}
}

// A resend after a long outage can carry an event the purge already deleted. Dedup on id
// cannot see it, so ingest must refuse it (and still answer 2xx) rather than store and count
// it a second time.
func TestEgressIngestRefusesEventsOlderThanRetention(t *testing.T) {
	ctx := context.Background()
	st, _, mgr := reaperLifecycleFixture(t)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	eg := newEgressAPI(mgr, "tok", "", &egressAuditDedup{}, nil)
	eg.retention = 30 * 24 * time.Hour
	eg.now = func() time.Time { return now }
	ms := func(d time.Time) string { return strconv.FormatInt(d.UnixMilli(), 10) }
	post := func(at time.Time, id string) {
		t.Helper()
		body := `{"events":[{"id":"` + id + `","at":` + ms(at) + `,"source":"10.0.1.5","host":"a.example","allowed":true,"count":1}]}`
		r := httptest.NewRequest("POST", "/internal/egress", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		eg.ingest(w, r)
		if w.Code != 204 {
			t.Fatalf("%s: want 204 so the gateway does not retry, got %d", id, w.Code)
		}
	}
	old := now.Add(-31 * 24 * time.Hour)
	post(old, "old:1")
	post(old, "old:1") // the resend of a purged event
	post(now.Add(-29*24*time.Hour), "fresh:1")

	if stats, _ := st.ListEgress(ctx, "2000-01-01", 10); len(stats) != 1 || stats[0].Allowed != 1 {
		t.Errorf("only the event inside the window may be counted, got %+v", stats)
	}
	got, err := st.ListAttributedEgressEvents(ctx, 0, 10)
	if err != nil || len(got) != 1 || got[0].ID != "fresh:1" {
		t.Errorf("stored events = %+v (%v), want only fresh:1", got, err)
	}

	// Retention 0 keeps everything, so nothing is refused as too old.
	eg.retention = 0
	post(old, "old:2")
	if got, _ := st.ListAttributedEgressEvents(ctx, 0, 10); len(got) != 2 {
		t.Errorf("retention 0 must not refuse old events, got %+v", got)
	}
}

// The purger deletes events past the window (plus the skew margin) and the address rows that
// ended before them, only while it holds the lease.
func TestEgressPurgerDeletesExpiredRowsUnderTheLease(t *testing.T) {
	ctx := context.Background()
	st, _, mgr := reaperLifecycleFixture(t)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	p := newEgressPurger(mgr, 30*24*time.Hour)
	p.now = func() time.Time { return now }
	p.batch, p.pause = 2, 0
	at := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }
	h := time.Hour
	for id, age := range map[string]time.Duration{
		"gone1": 31 * 24 * h, "gone2": 32 * 24 * h, "gone3": 33 * 24 * h,
		"edge":  30*24*h + h/2, // past the window but inside the skew margin
		"fresh": 24 * h,
	} {
		e := store.EgressEvent{ID: id, At: at(age), Source: "10.0.1.5", Host: "a.example", Allowed: true, Count: 1}
		if _, err := st.RecordEgressEvent(ctx, "2026-09-01", e, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpsertWorkspaceAddresses(ctx, "m", []store.WorkspaceAddress{
		{WorkspaceID: "w", Address: "10.0.1.5", Ref: "closed", From: at(41 * 24 * h), Until: at(35 * 24 * h)},
		{WorkspaceID: "w", Address: "10.0.1.5", Ref: "open", From: at(40 * 24 * h)},
	}); err != nil {
		t.Fatal(err)
	}

	// Another CP holds the lease: nothing is deleted.
	if ok, err := st.AcquireCPLease(ctx, egressPurgeLease, "other", time.Hour); err != nil || !ok {
		t.Fatalf("seed lease: %v %v", ok, err)
	}
	p.tick(ctx)
	if got, _ := st.ListAttributedEgressEvents(ctx, 0, 10); len(got) != 5 {
		t.Fatalf("a CP without the lease must not purge, got %d events", len(got))
	}
	if err := st.ReleaseCPLease(ctx, egressPurgeLease, "other"); err != nil {
		t.Fatal(err)
	}
	p.tick(ctx)
	got, _ := st.ListAttributedEgressEvents(ctx, 0, 10)
	ids := map[string]bool{}
	for _, e := range got {
		ids[e.ID] = true
	}
	if len(got) != 2 || !ids["edge"] || !ids["fresh"] {
		t.Errorf("events left = %v, want edge and fresh", ids)
	}
	if refs, _ := st.OpenWorkspaceAddressRefs(ctx, "m"); len(refs) != 1 {
		t.Errorf("open address row must stay, got %v", refs)
	}
	if n, _ := st.PurgeWorkspaceAddresses(ctx, now.UnixMilli(), 10); n != 0 {
		t.Errorf("the expired closed row must already be gone, %d left", n)
	}
}
