package store

import (
	"context"
	"errors"
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
		if err := st.ObserveWorkspaceAddresses(ctx, "mA", []WorkspaceAddress{{WorkspaceID: "wA", Address: addr, Ref: "taskA", From: 100}}, nil, 150); err != nil {
			t.Fatal(err)
		}
		// mA's task is gone when next looked at (closed at 300); mB's task got the address
		// at 250, before the close was seen.
		if err := st.ObserveWorkspaceAddresses(ctx, "mA", nil, nil, 300); err != nil {
			t.Fatal(err)
		}
		if err := st.ObserveWorkspaceAddresses(ctx, "mB", []WorkspaceAddress{{WorkspaceID: "wB", Address: addr, Ref: "taskB", From: 250}}, nil, 300); err != nil {
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

func openRefs(t *testing.T, st *SQL, m string) []string {
	t.Helper()
	refs, err := st.OpenWorkspaceAddressRefs(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	return refs
}

func TestObserveWorkspaceAddressesEndsOnlyMoveEarlier(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		row := WorkspaceAddress{WorkspaceID: "w1", Address: "10.0.1.7", Ref: "task-1", From: 100}
		if err := st.ObserveWorkspaceAddresses(ctx, "m1", []WorkspaceAddress{row}, nil, 110); err != nil {
			t.Fatal(err)
		}
		if got := openRefs(t, st, "m1"); !equalStrings(got, []string{"task-1"}) {
			t.Errorf("%s: open refs %v", name, got)
		}
		if err := st.ObserveWorkspaceAddresses(ctx, "m1", nil, nil, 300); err != nil {
			t.Fatal(err)
		}
		if got := openRefs(t, st, "m1"); len(got) != 0 {
			t.Errorf("%s: an address no longer reported must be closed, open %v", name, got)
		}
		// The task's reported stop time is earlier than the noticed absence: it wins.
		stopped := row
		stopped.Until = 200
		if err := st.ObserveWorkspaceAddresses(ctx, "m1", []WorkspaceAddress{stopped}, nil, 310); err != nil {
			t.Fatal(err)
		}
		// An older observation applied late (the task still running) must neither reopen the
		// row nor push its end back.
		if err := st.ObserveWorkspaceAddresses(ctx, "m1", []WorkspaceAddress{row}, nil, 320); err != nil {
			t.Fatal(err)
		}
		late := row
		late.Until = 250
		if err := st.ObserveWorkspaceAddresses(ctx, "m1", []WorkspaceAddress{late}, nil, 330); err != nil {
			t.Fatal(err)
		}
		if got := openRefs(t, st, "m1"); len(got) != 0 {
			t.Errorf("%s: a closed row was opened again: %v", name, got)
		}
		for _, e := range []EgressEvent{
			{ID: "during", At: 150, Source: row.Address, Count: 1},
			{ID: "after-stop", At: 210, Source: row.Address, Count: 1},
		} {
			if _, err := st.RecordEgressEvent(ctx, "d", e, 400); err != nil {
				t.Fatal(err)
			}
		}
		got, err := st.ListAttributedEgressEvents(ctx, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		holders := map[string]int{}
		for _, e := range got {
			holders[e.ID] = len(e.Holders)
		}
		if holders["during"] != 1 || holders["after-stop"] != 0 {
			t.Errorf("%s: the interval must end at the reported stop (200): %v", name, holders)
		}
		// Another membership's rows are never closed by this one's report.
		if err := st.ObserveWorkspaceAddresses(ctx, "m2", []WorkspaceAddress{{Address: "10.0.1.8", Ref: "other", From: 1}}, nil, 1); err != nil {
			t.Fatal(err)
		}
		if err := st.ObserveWorkspaceAddresses(ctx, "m1", nil, nil, 500); err != nil {
			t.Fatal(err)
		}
		if got, err := st.OpenAddressMemberships(ctx); err != nil || !equalStrings(got, []string{"m2"}) {
			t.Errorf("%s: open memberships %v, %v", name, got, err)
		}
	}
}

func TestObserveWorkspaceAddressesKeepsUnresolvedAndUpsertClosesNothing(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		a := WorkspaceAddress{Address: "10.0.1.5", Ref: "task-a", From: 100}
		b := WorkspaceAddress{Address: "10.0.1.6", Ref: "task-b", From: 200}
		if err := st.UpsertWorkspaceAddresses(ctx, "m1", []WorkspaceAddress{a}); err != nil {
			t.Fatal(err)
		}
		if err := st.UpsertWorkspaceAddresses(ctx, "m1", []WorkspaceAddress{b}); err != nil {
			t.Fatal(err)
		}
		if got := openRefs(t, st, "m1"); !equalStrings(got, []string{"task-a", "task-b"}) {
			t.Errorf("%s: an upsert must close nothing: %v", name, got)
		}
		// task-a could not be resolved this time: kept open. task-b is not reported: closed.
		if err := st.ObserveWorkspaceAddresses(ctx, "m1", nil, []string{"task-a"}, 300); err != nil {
			t.Fatal(err)
		}
		if got := openRefs(t, st, "m1"); !equalStrings(got, []string{"task-a"}) {
			t.Errorf("%s: open refs %v, want only the kept one", name, got)
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

// A purge deletes in bounded batches, oldest first, leaves newer events alone, and removes
// a closed address row only when it ended before the cutoff: an open row never goes.
func TestEgressPurgeIsBatchedAndKeepsWhatIsInWindow(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		for i := int64(1); i <= 7; i++ {
			e := EgressEvent{ID: "e" + string(rune('0'+i)), At: i * 100, Source: "10.0.1.5", Host: "a.example", Allowed: true, Count: 1}
			if _, err := st.RecordEgressEvent(ctx, "2026-10-24", e, 9); err != nil {
				t.Fatal(err)
			}
		}
		// Events at 100..500 are before the cutoff 600; at 600 and 700 they are not.
		var got []int
		for {
			n, err := st.PurgeEgressEvents(ctx, 600, 2)
			if err != nil {
				t.Fatalf("%s: purge: %v", name, err)
			}
			got = append(got, n)
			if n < 2 {
				break
			}
		}
		if want := []int{2, 2, 1}; len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
			t.Errorf("%s: batches = %v, want %v (at most the limit per statement)", name, got, want)
		}
		left, err := st.ListAttributedEgressEvents(ctx, 0, 10)
		if err != nil || len(left) != 2 || left[0].ID != "e7" || left[1].ID != "e6" {
			t.Errorf("%s: events left = %+v (%v), want e7 and e6", name, left, err)
		}
		if stats, _ := st.ListEgress(ctx, "2026-10-24", 10); len(stats) != 1 || stats[0].Allowed != 7 {
			t.Errorf("%s: egress_daily must keep the aggregate, got %+v", name, stats)
		}

		seen := []WorkspaceAddress{
			{WorkspaceID: "w", Address: "10.0.1.5", Ref: "old", From: 10, Until: 200},
			{WorkspaceID: "w", Address: "10.0.1.5", Ref: "edge", From: 300, Until: 600},
			{WorkspaceID: "w", Address: "10.0.1.5", Ref: "open", From: 50},
		}
		if err := st.UpsertWorkspaceAddresses(ctx, "m", seen); err != nil {
			t.Fatal(err)
		}
		n, err := st.PurgeWorkspaceAddresses(ctx, 600, 10)
		if err != nil || n != 1 {
			t.Fatalf("%s: purged %d address rows (%v), want only the one that ended before 600", name, n, err)
		}
		if refs, _ := st.OpenWorkspaceAddressRefs(ctx, "m"); !equalStrings(refs, []string{"open"}) {
			t.Errorf("%s: the open row must stay, got %v", name, refs)
		}
	}
}

// The floor a purge commits refuses older events from then on, on every store handle, and
// never goes back down.
func TestEgressFloorRefusesAndNeverLowers(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		e := EgressEvent{ID: "x", At: 100, Source: "10.0.1.5", Host: "a.example", Allowed: true, Count: 1}
		if ok, err := st.RecordEgressEvent(ctx, "2026-10-24", e, 1); err != nil || !ok {
			t.Fatalf("%s: first record: %v %v", name, ok, err)
		}
		if err := st.RaiseEgressEventFloor(ctx, 200); err != nil {
			t.Fatal(err)
		}
		if n, err := st.PurgeEgressEvents(ctx, 200, 10); err != nil || n != 1 {
			t.Fatalf("%s: purge %d %v", name, n, err)
		}
		if err := st.RaiseEgressEventFloor(ctx, 50); err != nil { // a longer retention
			t.Fatal(err)
		}
		if ok, err := st.RecordEgressEvent(ctx, "2026-10-24", e, 1); !errors.Is(err, ErrEgressEventExpired) || ok {
			t.Errorf("%s: purged event came back: %v %v", name, ok, err)
		}
		if stats, _ := st.ListEgress(ctx, "2026-10-24", 10); len(stats) != 1 || stats[0].Allowed != 1 {
			t.Errorf("%s: counted once, got %+v", name, stats)
		}
		fresh := e
		fresh.ID, fresh.At = "y", 300
		if ok, err := st.RecordEgressEvent(ctx, "2026-10-24", fresh, 1); err != nil || !ok {
			t.Errorf("%s: an event above the floor must be stored: %v %v", name, ok, err)
		}
	}
}
