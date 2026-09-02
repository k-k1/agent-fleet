package main

// 移送で main 側に残した 1 本（ADR 0067 / CP-STORE）。aggregateUsage は usage.go
// にある集計であって store ではない。DB も張らない純粋な関数のテストなので、
// internal/store/store_sqlite_test.go に同居していた理由は「同じパッケージ
// だったから」以上のものではなかった。

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
