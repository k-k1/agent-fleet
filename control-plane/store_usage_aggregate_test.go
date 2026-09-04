package main

// This test stays on the main side (ADR 0067 / CP-STORE): aggregateUsage is the
// aggregation in usage.go, not a store, and the test is a pure-function one that opens no
// database.

import (
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
	"testing"
)

// aggregateUsage must sum per member across days and compute hours.
func TestAggregateUsage(t *testing.T) {
	rows := []store.UsageRow{
		{TenantID: "default", TenantSlug: "default", MembershipID: "m1", UserKey: "a", Day: "2026-06-30", RunningSecs: 3600},
		{TenantID: "default", TenantSlug: "default", MembershipID: "m1", UserKey: "a", Day: "2026-07-01", RunningSecs: 1800},
		{TenantID: "default", TenantSlug: "default", MembershipID: "m2", UserKey: "b", Day: "2026-07-01", RunningSecs: 900},
	}
	got := aggregateUsage(rows)
	if len(got) != 2 {
		t.Fatalf("totals = %d, want 2 members", len(got))
	}
	if got[0].UserKey != "a" || got[0].RunningSecs != 5400 || got[0].RunningHrs != 1.5 {
		t.Fatalf("member a total wrong: %+v", got[0])
	}
	if got[1].UserKey != "b" || got[1].RunningHrs != 0.25 {
		t.Fatalf("member b total wrong: %+v", got[1])
	}
}
