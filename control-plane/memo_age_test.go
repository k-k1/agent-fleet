package main

import (
	"context"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// ageMemos moves every message that exists now back in time by d, so a message made after the
// call is newer than all of them. created_at has a second's precision and the queue orders by it
// first, so a test that needs "older" and "newer" used to sleep 1.1 s between the two; on Postgres
// (real time) that was real wall time, and on SQLite it only worked inside a bubble.
func (e *inboxEnv) ageMemos(d time.Duration) {
	e.t.Helper()
	ctx := context.Background()
	type row struct{ id, created, released string }
	rows, err := e.st.DB().QueryContext(ctx, `SELECT id, created_at, COALESCE(released_at,'') FROM memo`)
	if err != nil {
		e.t.Fatal(err)
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.created, &r.released); err != nil {
			rows.Close()
			e.t.Fatal(err)
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	rows.Close()
	shift := func(ts string) string {
		if ts == "" {
			return ""
		}
		at, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			e.t.Fatalf("memo timestamp %q: %v", ts, err)
		}
		return at.Add(-d).UTC().Format(time.RFC3339)
	}
	for _, r := range all {
		if _, err := e.st.DB().ExecContext(ctx, `UPDATE memo SET created_at=?, released_at=CASE WHEN ?='' THEN released_at ELSE ? END WHERE id=?`,
			shift(r.created), r.released, shift(r.released), r.id); err != nil {
			e.t.Fatal(err)
		}
	}
}

// The helper moves created_at and released_at together and leaves an unreleased message's
// released_at alone.
func TestAgeMemosShiftsBothStamps(t *testing.T) {
	e := newInboxEnv(t)
	pending := e.tidyRow("r", "q", "released", "member", "", "", store.MemoStatePending)
	e.ageMemos(2 * time.Second)
	got := e.memoRow(pending.ID)
	for _, c := range [][2]string{{pending.CreatedAt, got.CreatedAt}, {pending.ReleasedAt, got.ReleasedAt}} {
		a, _ := time.Parse(time.RFC3339, c[0])
		b, err := time.Parse(time.RFC3339, c[1])
		if err != nil || a.Sub(b) != 2*time.Second {
			t.Fatalf("%q -> %q (%v)", c[0], c[1], err)
		}
	}
}
