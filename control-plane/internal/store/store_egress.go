package store

import (
	"context"
)

// EgressStat is one destination host with its would-allow / would-block hit counts
// aggregated over the queried window (docs/log/20 M2, log-only egress proxy).
type EgressStat struct {
	Host             string
	Allowed, Blocked int
}

// AllowlistEntry is one versioned egress allowlist rule (docs/log/20 M3). TenantID is a
// tenant id, or "" for a deployment-global rule. State ∈ active | proposed | retired
// (proposed rules come from the M4 agent and need admin approval to go active).
type AllowlistEntry struct {
	ID, TenantID, Entry, State, Reason, AddedBy, AddedAt string
}

// EgressStore aggregates egress observation (docs/log/20 M2, log-only proxy) and
// holds the allowlist (M3). RecordEgress adds hits into the (day, host,
// allowed) bucket; ListEgress returns the busiest hosts on/after sinceDay
// (YYYY-MM-DD) with their would-allow / would-block split. ListAllowlist
// returns entries with the given state ("" = any); AddAllowlist inserts one;
// SetAllowlistState transitions a row (approve a proposed entry to active, or
// retire one); EffectiveAllowlist returns the active entry strings the proxy
// enforces.
type EgressStore interface {
	RecordEgress(ctx context.Context, day, host string, allowed bool, count int) error
	ListEgress(ctx context.Context, sinceDay string, limit int) ([]EgressStat, error)
	ListAllowlist(ctx context.Context, state string, limit int) ([]AllowlistEntry, error)
	AddAllowlist(ctx context.Context, e AllowlistEntry) error
	SetAllowlistState(ctx context.Context, id, state string) error
	EffectiveAllowlist(ctx context.Context) ([]string, error)
}

// RecordEgress accumulates egress hits into the (day, host, allowed) bucket
// (docs/log/20 M2). Upsert += so repeated batches add up.
func (s *SQL) RecordEgress(ctx context.Context, day, host string, allowed bool, count int) error {
	a := 0
	if allowed {
		a = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO egress_daily(day, host, allowed, count)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(day, host, allowed) DO UPDATE SET count = egress_daily.count + excluded.count`,
		day, host, a, count)
	return err
}

// ListEgress returns the busiest destination hosts on/after sinceDay, each with its
// would-allow / would-block totals, most-hit first (docs/log/20 M2).
//
// ORDER BY must not reference the SELECT aliases inside an expression: Postgres
// resolves names in an ORDER BY *expression* against the input columns only, so
// ORDER BY (allowed+blocked) failed there with 42703 (allowed is a real column,
// blocked is only an alias) while SQLite accepted it. SUM(count) is the same
// ordering key and is legal in both dialects.
func (s *SQL) ListEgress(ctx context.Context, sinceDay string, limit int) ([]EgressStat, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT host,
		        SUM(CASE WHEN allowed=1 THEN count ELSE 0 END) AS allowed,
		        SUM(CASE WHEN allowed=0 THEN count ELSE 0 END) AS blocked
		 FROM egress_daily WHERE day >= ?
		 GROUP BY host ORDER BY SUM(count) DESC, host ASC LIMIT ?`, sinceDay, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EgressStat
	for rows.Next() {
		var e EgressStat
		if err := rows.Scan(&e.Host, &e.Allowed, &e.Blocked); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- egress allowlist + deployment settings (docs/log/20 M3) --------------------

func (s *SQL) ListAllowlist(ctx context.Context, state string, limit int) ([]AllowlistEntry, error) {
	if limit <= 0 {
		limit = 500
	}
	q := `SELECT id, tenant_id, entry, state, reason, added_by, added_at FROM egress_allowlist`
	args := []any{}
	if state != "" {
		q += ` WHERE state=?`
		args = append(args, state)
	}
	q += ` ORDER BY added_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AllowlistEntry
	for rows.Next() {
		var e AllowlistEntry
		if err := rows.Scan(&e.ID, &e.TenantID, &e.Entry, &e.State, &e.Reason, &e.AddedBy, &e.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *SQL) AddAllowlist(ctx context.Context, e AllowlistEntry) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO egress_allowlist(id, tenant_id, entry, state, reason, added_by, added_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.TenantID, e.Entry, e.State, e.Reason, e.AddedBy, e.AddedAt)
	return err
}

func (s *SQL) SetAllowlistState(ctx context.Context, id, state string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE egress_allowlist SET state=? WHERE id=?`, state, id)
	return err
}

func (s *SQL) EffectiveAllowlist(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT entry FROM egress_allowlist WHERE state='active'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
