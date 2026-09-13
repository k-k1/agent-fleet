package store

// store_engine_usage.go — who the engine work was for (ADR 0079 open question 7).
//
// Everything else about engine consumption is recorded somewhere the deployment that CONSUMED
// it can read: the member's own ledger, a file inside their Workspace. A deployment that LENDS
// its engines has no such place — the borrowing membership is purpose-made and has no Workspace
// at all (ADR 0079 decision 3) — so the operator who paid for the card was left with a bill and
// no name. These two tables are that name.
//
// ⚠️ Neither is a second ledger. ADR 0029's ledger stays the file in the Workspace and nothing
// here feeds it; these answer one operator question and are pruned on the same retention as
// every other hourly bucket.

import "context"

// EngineUsageRow is an engine usage row the gateway could not hand to a Workspace, kept whole.
//
// It is the ledger's own vocabulary (feature / in / out / measured), because it IS the row the
// gateway built and then dropped — nothing here re-counts anything. `Session` is the caller's
// session name, which for a borrowed request is the BORROWER's session on another deployment
// (ADR 0079 decision 8); it is taken as stated and never verified, so it labels a row and
// authorises nothing.
type EngineUsageRow struct {
	ID           int64  `json:"id"`
	TS           string `json:"ts"` // RFC3339 (UTC) — when delivery was given up on, not when the call happened
	MembershipID string `json:"membership_id"`
	TenantID     string `json:"tenant_id,omitempty"`
	EngineKey    string `json:"engine_key"`
	// Reason is why it could not be delivered, and it is the operator's whole diagnosis:
	// `no_workspace` is the ordinary borrowing case and expected, while `no_identity` or
	// `post_failed` on a deployment nobody borrows from means rows are going missing.
	Reason   string `json:"reason,omitempty"`
	Feature  string `json:"feature,omitempty"`
	Provider string `json:"provider,omitempty"`
	Session  string `json:"session,omitempty"`
	Model    string `json:"model,omitempty"`
	In       int    `json:"in,omitempty"`
	Out      int    `json:"out,omitempty"`
	MS       int    `json:"ms,omitempty"`
	OK       bool   `json:"ok"`
	Measured string `json:"measured,omitempty"`
}

// EngineMembershipHourRow is one (engine, membership, hour) bucket.
//
// ⚠️ This is the ONLY count the image role has. An image answer carries no `usage` object, so
// there are no tokens to record and the gateway deliberately writes no ledger row for it
// (ADR 0069 decision 9, ADR 0071 decision 9, ADR 0076 decision 8). Requests and milliseconds
// are what is honestly measurable for both roles, so both are counted here — including the
// requests that DID reach a Workspace ledger, because an operator asking "whose box was that"
// cannot be made to read two tables and add them up.
// The identity columns are LEFT-JOINed on read, exactly as UsageHourRow does it: a membership
// id alone does not answer "whose box was that" for a human, and a membership that has since
// been deleted still has rows worth showing — so they are joined rather than denormalised, and
// they come back empty when the join finds nothing.
type EngineMembershipHourRow struct {
	EngineKey    string `json:"engine_key"`
	MembershipID string `json:"membership_id"`
	Hour         string `json:"hour"` // YYYY-MM-DDTHH (UTC) — the client shifts to local time
	TenantID     string `json:"tenant_id,omitempty"`
	TenantSlug   string `json:"tenant_slug,omitempty"`
	UserKey      string `json:"user_key,omitempty"`
	Email        string `json:"email,omitempty"`
	EngineMembershipHourCounters
}

// EngineMembershipHourCounters is what one hour accumulates. Split out from the row so the
// gateway's per-request delta and the stored total are literally the same shape.
//
// The json tags carry omitempty because these ride the API as-is.
type EngineMembershipHourCounters struct {
	Requests   int `json:"requests,omitempty"`
	OKRequests int `json:"ok_requests,omitempty"`
	MS         int `json:"ms,omitempty"`
	In         int `json:"in,omitempty"`
	Out        int `json:"out,omitempty"`
}

// EngineUsageAttributionStore is the far side's answer to "whose work was that GPU doing".
type EngineUsageAttributionStore interface {
	// AddEngineUsageUndelivered keeps a row the gateway could not deliver. Never fails a
	// request: the caller is already in a detached goroutine doing bookkeeping.
	AddEngineUsageUndelivered(ctx context.Context, row EngineUsageRow) error
	// ListEngineUsageUndelivered returns rows in [fromTS, toTS], newest first. An empty
	// membershipID or engineKey means "every one".
	ListEngineUsageUndelivered(ctx context.Context, membershipID, engineKey, fromTS, toTS string, limit int) ([]EngineUsageRow, error)
	PruneEngineUsageUndelivered(ctx context.Context, beforeTS string) error

	// AddEngineMembershipHour accumulates one relayed request into its bucket.
	AddEngineMembershipHour(ctx context.Context, engineKey, membershipID, tenantID, hour string, d EngineMembershipHourCounters) error
	// ListEngineMembershipHourly returns buckets in [fromHour, toHour] (inclusive). An empty
	// engineKey means "every engine".
	ListEngineMembershipHourly(ctx context.Context, engineKey, fromHour, toHour string) ([]EngineMembershipHourRow, error)
	PruneEngineMembershipHourly(ctx context.Context, beforeHour string) error
}

// AddEngineUsageUndelivered keeps one dropped row. Append-only: two identical calls a second
// apart are two facts, not one row written twice, so there is no ON CONFLICT here.
func (s *SQL) AddEngineUsageUndelivered(ctx context.Context, row EngineUsageRow) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO engine_usage_undelivered(ts, membership_id, tenant_id, engine_key, reason,
		                                      feature, provider, session, model,
		                                      in_tokens, out_tokens, ms, ok, measured)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.TS, row.MembershipID, row.TenantID, row.EngineKey, row.Reason,
		row.Feature, row.Provider, row.Session, row.Model,
		row.In, row.Out, row.MS, b2i(row.OK), row.Measured)
	return err
}

func (s *SQL) ListEngineUsageUndelivered(ctx context.Context, membershipID, engineKey, fromTS, toTS string, limit int) ([]EngineUsageRow, error) {
	q := `SELECT id, ts, membership_id, tenant_id, engine_key, reason,
	             feature, provider, session, model, in_tokens, out_tokens, ms, ok, measured
	      FROM engine_usage_undelivered WHERE ts BETWEEN ? AND ?`
	args := []any{fromTS, toTS}
	// "" is the sentinel for "every one", the same way ListEngineHourly reads an empty engine
	// key — a filter nobody set must not become a filter that matches nothing.
	if membershipID != "" {
		q += ` AND membership_id=?`
		args = append(args, membershipID)
	}
	if engineKey != "" {
		q += ` AND engine_key=?`
		args = append(args, engineKey)
	}
	q += ` ORDER BY ts DESC, id DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EngineUsageRow
	for rows.Next() {
		var e EngineUsageRow
		// database/sql does not convert int<->bool on its own, which is why `ok` is an
		// INTEGER column in both dialects (b2i on the way in).
		var ok int
		if err := rows.Scan(&e.ID, &e.TS, &e.MembershipID, &e.TenantID, &e.EngineKey, &e.Reason,
			&e.Feature, &e.Provider, &e.Session, &e.Model, &e.In, &e.Out, &e.MS, &ok, &e.Measured); err != nil {
			return nil, err
		}
		e.OK = ok != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *SQL) PruneEngineUsageUndelivered(ctx context.Context, beforeTS string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM engine_usage_undelivered WHERE ts < ?`, beforeTS)
	return err
}

// AddEngineMembershipHour accumulates one relayed request. Everything sums; there is no peak
// column, so no CASE-based max is needed here (MAX/GREATEST differ between the two dialects
// and this file has to be one statement for both).
func (s *SQL) AddEngineMembershipHour(ctx context.Context, engineKey, membershipID, tenantID, hour string, d EngineMembershipHourCounters) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO engine_membership_hourly(engine_key, membership_id, hour, tenant_id,
		                                      requests, ok_requests, ms, in_tokens, out_tokens)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(engine_key, membership_id, hour) DO UPDATE SET
		   requests    = engine_membership_hourly.requests    + excluded.requests,
		   ok_requests = engine_membership_hourly.ok_requests + excluded.ok_requests,
		   ms          = engine_membership_hourly.ms          + excluded.ms,
		   in_tokens   = engine_membership_hourly.in_tokens   + excluded.in_tokens,
		   out_tokens  = engine_membership_hourly.out_tokens  + excluded.out_tokens`,
		engineKey, membershipID, hour, tenantID,
		d.Requests, d.OKRequests, d.MS, d.In, d.Out)
	return err
}

func (s *SQL) ListEngineMembershipHourly(ctx context.Context, engineKey, fromHour, toHour string) ([]EngineMembershipHourRow, error) {
	q := `SELECT h.engine_key, h.membership_id, h.hour, h.tenant_id,
	             COALESCE(t.slug,''), COALESCE(i.user_key,''), COALESCE(i.email,''),
	             h.requests, h.ok_requests, h.ms, h.in_tokens, h.out_tokens
	      FROM engine_membership_hourly h
	      LEFT JOIN tenant t ON t.id = h.tenant_id
	      LEFT JOIN membership m ON m.id = h.membership_id
	      LEFT JOIN identity i ON i.id = m.identity_id
	      WHERE h.hour BETWEEN ? AND ?`
	args := []any{fromHour, toHour}
	if engineKey != "" {
		q += ` AND h.engine_key=?`
		args = append(args, engineKey)
	}
	q += ` ORDER BY h.engine_key, h.membership_id, h.hour`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EngineMembershipHourRow
	for rows.Next() {
		var e EngineMembershipHourRow
		if err := rows.Scan(&e.EngineKey, &e.MembershipID, &e.Hour, &e.TenantID,
			&e.TenantSlug, &e.UserKey, &e.Email,
			&e.Requests, &e.OKRequests, &e.MS, &e.In, &e.Out); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *SQL) PruneEngineMembershipHourly(ctx context.Context, beforeHour string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM engine_membership_hourly WHERE hour < ?`, beforeHour)
	return err
}
