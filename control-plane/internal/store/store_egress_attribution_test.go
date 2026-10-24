package store

import (
	"context"
	"testing"
)

func TestEgressEventIsStoredAndCountedOnce(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		e := EgressEvent{ID: "gw1:4096", At: 1000, Source: "10.0.1.5", Dest: "140.82.112.3", Host: "github.com", Allowed: true, Count: 1}
		for i, want := range []bool{true, false, false} {
			got, err := st.RecordEgressEvent(ctx, "2026-10-24", e, 2000)
			if err != nil {
				t.Fatalf("%s: record %d: %v", name, i, err)
			}
			if got != want {
				t.Errorf("%s: record %d: inserted=%v, want %v", name, i, got, want)
			}
		}
		stats, err := st.ListEgress(ctx, "2026-10-24", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(stats) != 1 || stats[0].Allowed != 1 {
			t.Errorf("%s: a resent event must be counted once, got %+v", name, stats)
		}
	}
}

// An address held by one workspace, released, and handed to another: each event goes to
// the workspace that held the address at its time, and the window in which both rows are
// open is reported with both.
func TestEgressAttributionFollowsAddressReuse(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		const addr = "10.0.1.5"
		if err := st.ObserveWorkspaceAddresses(ctx, "mA", []WorkspaceAddress{{WorkspaceID: "wA", Address: addr, Ref: "taskA", From: 100}}, 150); err != nil {
			t.Fatal(err)
		}
		// mA's task is gone when next looked at (closed at 300); mB's task got the address
		// at 250, before the close was seen.
		if err := st.ObserveWorkspaceAddresses(ctx, "mA", nil, 300); err != nil {
			t.Fatal(err)
		}
		if err := st.ObserveWorkspaceAddresses(ctx, "mB", []WorkspaceAddress{{WorkspaceID: "wB", Address: addr, Ref: "taskB", From: 250}}, 300); err != nil {
			t.Fatal(err)
		}
		for _, e := range []EgressEvent{
			{ID: "e-before", At: 50, Source: addr, Host: "a.example", Allowed: true, Count: 1},
			{ID: "e-a", At: 200, Source: addr, Host: "a.example", Allowed: true, Count: 1},
			{ID: "e-both", At: 260, Source: addr, Host: "a.example", Allowed: true, Count: 1},
			{ID: "e-b", At: 400, Source: addr, Host: "a.example", Allowed: true, Count: 1},
		} {
			if _, err := st.RecordEgressEvent(ctx, "2026-10-24", e, 500); err != nil {
				t.Fatal(err)
			}
		}
		got, err := st.ListAttributedEgressEvents(ctx, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		holders := map[string][]string{}
		for _, e := range got {
			holders[e.ID] = []string{}
			for _, h := range e.Holders {
				holders[e.ID] = append(holders[e.ID], h.MembershipID)
			}
		}
		want := map[string][]string{"e-before": {}, "e-a": {"mA"}, "e-both": {"mA", "mB"}, "e-b": {"mB"}}
		for id, w := range want {
			if g, ok := holders[id]; !ok || !equalStrings(g, w) {
				t.Errorf("%s: %s holders = %v, want %v", name, id, g, w)
			}
		}
		if len(got) != 4 || got[0].ID != "e-b" {
			t.Errorf("%s: want 4 events newest first, got %+v", name, got)
		}
	}
}

func TestObserveWorkspaceAddressesClosesAndReopens(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		row := WorkspaceAddress{WorkspaceID: "w1", Address: "10.0.1.7", Ref: "pod-uid", From: 100}
		if err := st.ObserveWorkspaceAddresses(ctx, "m1", []WorkspaceAddress{row}, 110); err != nil {
			t.Fatal(err)
		}
		if open, _ := st.HasOpenWorkspaceAddress(ctx, "m1"); !open {
			t.Errorf("%s: a reported address must be open", name)
		}
		if err := st.ObserveWorkspaceAddresses(ctx, "m1", nil, 120); err != nil {
			t.Fatal(err)
		}
		if open, _ := st.HasOpenWorkspaceAddress(ctx, "m1"); open {
			t.Errorf("%s: an address no longer reported must be closed", name)
		}
		// Seen again with the same incarnation: the close was wrong, open it again.
		if err := st.ObserveWorkspaceAddresses(ctx, "m1", []WorkspaceAddress{row}, 130); err != nil {
			t.Fatal(err)
		}
		if open, _ := st.HasOpenWorkspaceAddress(ctx, "m1"); !open {
			t.Errorf("%s: the same holder seen again must be open again", name)
		}
		// A reported end (a stopped ECS task) closes the row at that time, not at now.
		row.Until = 125
		if err := st.ObserveWorkspaceAddresses(ctx, "m1", []WorkspaceAddress{row}, 140); err != nil {
			t.Fatal(err)
		}
		if _, err := st.RecordEgressEvent(ctx, "d", EgressEvent{ID: "x", At: 130, Source: row.Address, Count: 1}, 150); err != nil {
			t.Fatal(err)
		}
		got, err := st.ListAttributedEgressEvents(ctx, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || len(got[0].Holders) != 0 {
			t.Errorf("%s: an event after the reported stop must be unattributed, got %+v", name, got)
		}
		// Another membership's rows are never closed by this one's report.
		if err := st.ObserveWorkspaceAddresses(ctx, "m2", []WorkspaceAddress{{Address: "10.0.1.8", Ref: "other", From: 1}}, 1); err != nil {
			t.Fatal(err)
		}
		if err := st.ObserveWorkspaceAddresses(ctx, "m1", nil, 200); err != nil {
			t.Fatal(err)
		}
		if open, _ := st.HasOpenWorkspaceAddress(ctx, "m2"); !open {
			t.Errorf("%s: m1's report closed m2's address", name)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
