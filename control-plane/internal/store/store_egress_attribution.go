package store

import (
	"context"
	"strconv"
	"strings"
)

// EgressEvent is one gateway observation kept as it arrived (ADR 0122, addendum
// 2026-10-24). ID is the sender's own key for the observation, so a batch it resends after
// a failed POST lands once. At is unix milliseconds.
type EgressEvent struct {
	ID, Source, Dest, Host string
	At                     int64
	Allowed                bool
	Count                  int
}

// WorkspaceAddress is one interval during which a workspace held an address, as its
// orchestrator reported it. Ref names the holder (task ARN, pod UID, container id) and,
// with From, one incarnation of it. Until 0 means the address is still held. Times are unix
// milliseconds.
type WorkspaceAddress struct {
	MembershipID, WorkspaceID, Address, Ref string
	From, Until                             int64
}

// AttributedEgressEvent is an event with every address row whose interval contains it.
// Zero holders is unattributed and more than one is ambiguous; the store does not choose.
type AttributedEgressEvent struct {
	EgressEvent
	Holders []WorkspaceAddress
}

// EgressAttributionStore keeps raw egress events and the address intervals they are
// attributed through.
type EgressAttributionStore interface {
	// RecordEgressEvent stores e unless an event with its ID is already stored, and adds it
	// to egress_daily under day only when it was new. inserted reports which happened.
	RecordEgressEvent(ctx context.Context, day string, e EgressEvent, receivedAt int64) (inserted bool, err error)
	// ListAttributedEgressEvents returns the newest events at or after since, each with the
	// address rows that held its source at its time.
	ListAttributedEgressEvents(ctx context.Context, since int64, limit int) ([]AttributedEgressEvent, error)
	// ObserveWorkspaceAddresses records what one membership's runtime reports now: a reported
	// row is inserted, or has its end set or moved earlier, and every row of the membership
	// still open but not reported is closed at now. A closed row is never opened again: the
	// observation that would reopen it can only be an older one applied late.
	ObserveWorkspaceAddresses(ctx context.Context, membershipID string, seen []WorkspaceAddress, now int64) error
	// OpenWorkspaceAddressRefs returns the refs of the membership's rows still open.
	OpenWorkspaceAddressRefs(ctx context.Context, membershipID string) ([]string, error)
	// OpenAddressMemberships returns every membership that has a row still open.
	OpenAddressMemberships(ctx context.Context) ([]string, error)
}

func (s *SQL) RecordEgressEvent(ctx context.Context, day string, e EgressEvent, receivedAt int64) (bool, error) {
	a := 0
	if e.Allowed {
		a = 1
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO egress_event(id, at, source, dest, host, allowed, count, received_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
		e.ID, e.At, e.Source, e.Dest, e.Host, a, e.Count, receivedAt)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	// The aggregate is written in the same transaction: an event stored without its count
	// would be skipped as a duplicate on the retry and never counted.
	if e.Host != "" {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO egress_daily(day, host, allowed, count)
			 VALUES(?, ?, ?, ?)
			 ON CONFLICT(day, host, allowed) DO UPDATE SET count = egress_daily.count + excluded.count`,
			day, e.Host, a, e.Count); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

func (s *SQL) ListAttributedEgressEvents(ctx context.Context, since int64, limit int) ([]AttributedEgressEvent, error) {
	if limit <= 0 {
		limit = 200
	}
	// The limit applies to events, so it sits in the subquery: a join first would cut an
	// ambiguous event's holders short.
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.id, e.at, e.source, e.dest, e.host, e.allowed, e.count,
		        COALESCE(a.membership_id, ''), COALESCE(a.workspace_id, ''), COALESCE(a.ref, ''),
		        COALESCE(a.from_at, 0), COALESCE(a.until_at, 0)
		 FROM (SELECT * FROM egress_event WHERE at >= ? ORDER BY at DESC, id ASC LIMIT ?) e
		 LEFT JOIN workspace_address a
		   ON a.address = e.source AND a.from_at <= e.at AND (a.until_at = 0 OR e.at < a.until_at)
		 ORDER BY e.at DESC, e.id ASC, a.from_at ASC, a.ref ASC`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AttributedEgressEvent
	for rows.Next() {
		var e EgressEvent
		var h WorkspaceAddress
		var allowed int
		if err := rows.Scan(&e.ID, &e.At, &e.Source, &e.Dest, &e.Host, &allowed, &e.Count,
			&h.MembershipID, &h.WorkspaceID, &h.Ref, &h.From, &h.Until); err != nil {
			return nil, err
		}
		e.Allowed = allowed == 1
		if len(out) == 0 || out[len(out)-1].ID != e.ID {
			out = append(out, AttributedEgressEvent{EgressEvent: e})
		}
		if h.Ref != "" {
			h.Address = e.Source
			last := &out[len(out)-1]
			last.Holders = append(last.Holders, h)
		}
	}
	return out, rows.Err()
}

func (s *SQL) ObserveWorkspaceAddresses(ctx context.Context, membershipID string, seen []WorkspaceAddress, now int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	keep := map[string]bool{}
	for _, a := range seen {
		// An end only ever appears or moves earlier: a stopped task's reported stop time
		// replaces the later time its absence was noticed, and a report without an end
		// changes nothing on a row already closed.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO workspace_address(membership_id, workspace_id, address, ref, from_at, until_at)
			 VALUES(?, ?, ?, ?, ?, ?)
			 ON CONFLICT(ref, address, from_at) DO UPDATE SET until_at = CASE
			   WHEN excluded.until_at <> 0 AND (workspace_address.until_at = 0 OR excluded.until_at < workspace_address.until_at)
			   THEN excluded.until_at ELSE workspace_address.until_at END`,
			membershipID, a.WorkspaceID, a.Address, a.Ref, a.From, a.Until); err != nil {
			return err
		}
		keep[addressKey(a.Ref, a.Address, a.From)] = true
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT ref, address, from_at FROM workspace_address WHERE membership_id=? AND until_at=0`, membershipID)
	if err != nil {
		return err
	}
	var gone []WorkspaceAddress
	for rows.Next() {
		var a WorkspaceAddress
		if err := rows.Scan(&a.Ref, &a.Address, &a.From); err != nil {
			rows.Close()
			return err
		}
		if !keep[addressKey(a.Ref, a.Address, a.From)] {
			gone = append(gone, a)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, a := range gone {
		// Closed when it was seen gone, which is later than it went: an overlap with the next
		// holder of the address then shows as ambiguous, never as the wrong workspace.
		if _, err := tx.ExecContext(ctx,
			`UPDATE workspace_address SET until_at=? WHERE ref=? AND address=? AND from_at=? AND until_at=0`,
			max(now, a.From+1), a.Ref, a.Address, a.From); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQL) OpenWorkspaceAddressRefs(ctx context.Context, membershipID string) ([]string, error) {
	return s.queryStrings(ctx,
		`SELECT DISTINCT ref FROM workspace_address WHERE membership_id=? AND until_at=0 ORDER BY ref`, membershipID)
}

func (s *SQL) OpenAddressMemberships(ctx context.Context) ([]string, error) {
	return s.queryStrings(ctx,
		`SELECT DISTINCT membership_id FROM workspace_address WHERE until_at=0 ORDER BY membership_id`)
}

func (s *SQL) queryStrings(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func addressKey(ref, address string, from int64) string {
	var b strings.Builder
	b.WriteString(ref)
	b.WriteByte(0)
	b.WriteString(address)
	b.WriteByte(0)
	b.WriteString(strconv.FormatInt(from, 10))
	return b.String()
}
