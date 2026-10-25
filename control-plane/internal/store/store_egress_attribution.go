package store

import (
	"context"
	"database/sql"
	"errors"
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

// ErrEgressEventExpired is RecordEgressEvent's answer for an event older than the floor the
// purge has raised: it may be the resend of one already deleted, so it is neither stored nor
// counted.
var ErrEgressEventExpired = errors.New("egress event is older than the purged floor")

// egressFloorCounter is the cp_counter row holding the floor: the newest cutoff any purge
// has deleted up to, in unix milliseconds. It only ever rises.
const egressFloorCounter = "egress-event-floor"

// EgressAttributionStore keeps raw egress events and the address intervals they are
// attributed through.
type EgressAttributionStore interface {
	// RecordEgressEvent stores e unless an event with its ID is already stored, and adds it
	// to egress_daily under day only when it was new. inserted reports which happened. An
	// event older than the purge floor is refused with ErrEgressEventExpired: the floor is
	// read in the same transaction, so every CP honours what any other has purged.
	// RaiseEgressEventFloor commits the cutoff a purge is about to delete up to. It must run
	// before the delete and never lowers the floor, so a longer or disabled retention later
	// cannot let a purged event in again.
	RaiseEgressEventFloor(ctx context.Context, before int64) error
	RecordEgressEvent(ctx context.Context, day string, e EgressEvent, receivedAt int64) (inserted bool, err error)
	// ListAttributedEgressEvents returns the newest events at or after since, each with the
	// address rows that held its source at its time.
	ListAttributedEgressEvents(ctx context.Context, since int64, limit int) ([]AttributedEgressEvent, error)
	// ObserveWorkspaceAddresses records what one membership's runtime reports now: a reported
	// row is inserted, or has its end set or moved earlier, and every row of the membership
	// still open, not reported and whose ref is not in keep is closed at now. A closed row is
	// never opened again: the observation that would reopen it can only be an older one
	// applied late.
	ObserveWorkspaceAddresses(ctx context.Context, membershipID string, seen []WorkspaceAddress, keep []string, now int64) error
	// UpsertWorkspaceAddresses records reported rows as ObserveWorkspaceAddresses does and
	// closes nothing: for a partial view, such as the addresses a Stop is about to release.
	UpsertWorkspaceAddresses(ctx context.Context, membershipID string, seen []WorkspaceAddress) error
	// OpenWorkspaceAddressRefs returns the refs of the membership's rows still open.
	OpenWorkspaceAddressRefs(ctx context.Context, membershipID string) ([]string, error)
	// OpenAddressMemberships returns every membership that has a row still open.
	OpenAddressMemberships(ctx context.Context) ([]string, error)
	// PurgeEgressEvents deletes at most limit raw events whose time is before the cutoff
	// (unix milliseconds), oldest first, and returns how many it deleted. One call is one
	// bounded statement: the caller loops until it returns less than limit.
	PurgeEgressEvents(ctx context.Context, before int64, limit int) (int, error)
	// PurgeWorkspaceAddresses deletes at most limit closed address rows that ended before the
	// cutoff and returns how many. A row still open is never deleted: it has no end.
	PurgeWorkspaceAddresses(ctx context.Context, before int64, limit int) (int, error)
	// OldestEgressEvent returns the time of the oldest raw event; ok is false when there is none.
	OldestEgressEvent(ctx context.Context) (at int64, ok bool, err error)
	// EachPurgeableWorkspaceAddress calls fn for every closed address row that ended before
	// the cutoff, in (from, ref, address) order: the rows PurgeWorkspaceAddresses would delete.
	EachPurgeableWorkspaceAddress(ctx context.Context, before int64, fn func(WorkspaceAddress) error) error
	// DeleteWorkspaceAddressVersions deletes exactly the given rows, each only while its end
	// still equals the one given, and returns how many went. A row that arrived or changed
	// after the caller read it is not matched, so it is never deleted unseen.
	DeleteWorkspaceAddressVersions(ctx context.Context, rows []WorkspaceAddress) (int, error)
	// EachEgressEvent calls fn for every event with from <= at < to in (at, id) order. It
	// pages by key, batch rows per query, so a day of events is never held in memory and no
	// cursor stays open between calls. An error from fn stops the walk and is returned.
	EachEgressEvent(ctx context.Context, from, to int64, batch int, fn func(EgressEvent) error) error
	// EachWorkspaceAddressOverlapping calls fn for every address row whose interval meets
	// [from, to): the rows an event inside that window can be attributed through.
	EachWorkspaceAddressOverlapping(ctx context.Context, from, to int64, fn func(WorkspaceAddress) error) error
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
	floor, err := s.egressFloor(ctx, tx)
	if err != nil {
		return false, err
	}
	if e.At < floor {
		return false, ErrEgressEventExpired
	}
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

// egressFloor reads the floor inside tx. On Postgres the row is locked FOR SHARE, which a
// purge raising it waits for: an ingest that read the old floor commits before the purge
// deletes, so its row is deleted rather than re-stored after the delete. The row is created
// on first use so there is always one to lock.
func (s *SQL) egressFloor(ctx context.Context, tx *sqlTx) (int64, error) {
	lock := ""
	if s.dialect == "postgres" {
		lock = " FOR SHARE"
	}
	q := `SELECT value FROM cp_counter WHERE name=?` + lock
	var v int64
	err := tx.QueryRowContext(ctx, q, egressFloorCounter).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO cp_counter(name, value) VALUES(?, 0) ON CONFLICT(name) DO NOTHING`, egressFloorCounter); err != nil {
			return 0, err
		}
		err = tx.QueryRowContext(ctx, q, egressFloorCounter).Scan(&v)
	}
	return v, err
}

func (s *SQL) RaiseEgressEventFloor(ctx context.Context, before int64) error {
	greatest := "MAX"
	if s.dialect == "postgres" {
		greatest = "GREATEST"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cp_counter(name, value) VALUES(?, ?)
		 ON CONFLICT(name) DO UPDATE SET value = `+greatest+`(cp_counter.value, excluded.value)`,
		egressFloorCounter, before)
	return err
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

func (s *SQL) UpsertWorkspaceAddresses(ctx context.Context, membershipID string, seen []WorkspaceAddress) error {
	return s.observeWorkspaceAddresses(ctx, membershipID, seen, nil, 0, false)
}

func (s *SQL) ObserveWorkspaceAddresses(ctx context.Context, membershipID string, seen []WorkspaceAddress, keep []string, now int64) error {
	return s.observeWorkspaceAddresses(ctx, membershipID, seen, keep, now, true)
}

func (s *SQL) observeWorkspaceAddresses(ctx context.Context, membershipID string, seen []WorkspaceAddress, keepRefs []string, now int64, closeRest bool) error {
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
	if !closeRest {
		return tx.Commit()
	}
	kept := map[string]bool{}
	for _, r := range keepRefs {
		kept[r] = true
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
		if !keep[addressKey(a.Ref, a.Address, a.From)] && !kept[a.Ref] {
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

func (s *SQL) PurgeEgressEvents(ctx context.Context, before int64, limit int) (int, error) {
	// The id subquery keeps the delete to one batch on both dialects (a bare DELETE ... LIMIT
	// is SQLite-only) and walks egress_event_at, so the oldest rows go first.
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM egress_event WHERE id IN
		 (SELECT id FROM egress_event WHERE at < ? ORDER BY at LIMIT ?)`, before, limit)
	return rowsDeleted(res, err)
}

func (s *SQL) PurgeWorkspaceAddresses(ctx context.Context, before int64, limit int) (int, error) {
	// No index covers until_at; the table holds one row per address interval, not per
	// connection, so the scan stays small next to egress_event.
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM workspace_address WHERE (ref, address, from_at) IN
		 (SELECT ref, address, from_at FROM workspace_address WHERE until_at > 0 AND until_at < ? LIMIT ?)`,
		before, limit)
	return rowsDeleted(res, err)
}

func rowsDeleted(res sql.Result, err error) (int, error) {
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
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

func (s *SQL) oldest(ctx context.Context, q string, args ...any) (int64, bool, error) {
	var v sql.NullInt64
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&v); err != nil {
		return 0, false, err
	}
	return v.Int64, v.Valid, nil
}

func (s *SQL) OldestEgressEvent(ctx context.Context) (int64, bool, error) {
	return s.oldest(ctx, `SELECT MIN(at) FROM egress_event`)
}

func (s *SQL) EachPurgeableWorkspaceAddress(ctx context.Context, before int64, fn func(WorkspaceAddress) error) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT membership_id, workspace_id, address, ref, from_at, until_at FROM workspace_address
		 WHERE until_at > 0 AND until_at < ? ORDER BY from_at, ref, address`, before)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a WorkspaceAddress
		if err := rows.Scan(&a.MembershipID, &a.WorkspaceID, &a.Address, &a.Ref, &a.From, &a.Until); err != nil {
			return err
		}
		if err := fn(a); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *SQL) DeleteWorkspaceAddressVersions(ctx context.Context, rows []WorkspaceAddress) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	total := 0
	for _, a := range rows {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM workspace_address WHERE ref=? AND address=? AND from_at=? AND until_at=?`,
			a.Ref, a.Address, a.From, a.Until)
		n, err := rowsDeleted(res, err)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, tx.Commit()
}

func (s *SQL) EachEgressEvent(ctx context.Context, from, to int64, batch int, fn func(EgressEvent) error) error {
	if batch <= 0 {
		batch = 5000
	}
	// The row-value comparison walks egress_event_at from the last key; an empty id sorts
	// before every real one, so the first page starts at from.
	afterAt, afterID := from, ""
	for {
		rows, err := s.db.QueryContext(ctx,
			`SELECT id, at, source, dest, host, allowed, count FROM egress_event
			 WHERE at < ? AND (at, id) > (?, ?) ORDER BY at, id LIMIT ?`, to, afterAt, afterID, batch)
		if err != nil {
			return err
		}
		n := 0
		for rows.Next() {
			var e EgressEvent
			var allowed int
			if err := rows.Scan(&e.ID, &e.At, &e.Source, &e.Dest, &e.Host, &allowed, &e.Count); err != nil {
				rows.Close()
				return err
			}
			e.Allowed = allowed == 1
			afterAt, afterID = e.At, e.ID
			n++
			if err := fn(e); err != nil {
				rows.Close()
				return err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if n < batch {
			return nil
		}
	}
}

func (s *SQL) EachWorkspaceAddressOverlapping(ctx context.Context, from, to int64, fn func(WorkspaceAddress) error) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT membership_id, workspace_id, address, ref, from_at, until_at FROM workspace_address
		 WHERE from_at < ? AND (until_at = 0 OR until_at > ?) ORDER BY from_at, ref, address`, to, from)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a WorkspaceAddress
		if err := rows.Scan(&a.MembershipID, &a.WorkspaceID, &a.Address, &a.Ref, &a.From, &a.Until); err != nil {
			return err
		}
		if err := fn(a); err != nil {
			return err
		}
	}
	return rows.Err()
}
