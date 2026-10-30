package store

import (
	"context"
)

// UsageRow is one (membership, day) showback bucket enriched with human labels
// for reporting (docs/log/roadmap.md P3-9). RunningSecs is accumulated workspace
// occupancy in seconds. A member with no membership record still surfaces (its
// workspace outlived the membership) with empty UserKey/Email.
type UsageRow struct {
	TenantID, TenantSlug, MembershipID, UserKey, Email, Day string
	RunningSecs                                             int
}

// UsageHourRow is one (membership, hour) occupancy bucket — the same claim as UsageRow
// at hour resolution, plus how many sessions were open inside the running workspace
// (docs/log/83). Labels are joined at read time exactly like UsageRow.
//
// ⚠️ MembershipID=="" is the sampler HEARTBEAT for that hour, not a member: it records
// that a sweep completed, which is what makes "observed, not running" (grey) different
// from "never observed" (blank). Aggregation must skip it — summing it into a fleet
// total would double-count nothing real, but rendering it as a member would invent one.
type UsageHourRow struct {
	TenantID, TenantSlug, MembershipID, UserKey, Email, Hour string
	UsageHourCounters
}

// UsageHourCounters is what one hour accumulates. Split out from the row so the
// sampler's per-sample delta and the stored total are literally the same shape —
// AddUsageHour adds the sums and takes the max of the peaks, field by field.
//
// ⚠️ MeasuredSecs is not RunningSecs. Session counters accumulate only while the Agent
// actually answered, so RunningSecs>0 with MeasuredSecs==0 means "running, session
// count unknown" and must not be read as zero sessions.
// The json tags carry omitempty because these ride the API as-is: a window is up to
// 24x30 buckets, and an hour where nothing was busy should not spend seven zeros
// saying so.
type UsageHourCounters struct {
	Samples      int `json:"samples,omitempty"`
	RunningSecs  int `json:"running_secs,omitempty"`
	MeasuredSecs int `json:"measured_secs,omitempty"`
	SessionSecs  int `json:"session_secs,omitempty"`
	BusySecs     int `json:"busy_secs,omitempty"`
	MaxSessions  int `json:"max_sessions,omitempty"`
	MaxBusy      int `json:"max_busy,omitempty"`
}

// EngineHourRow is one (engine, hour) occupancy bucket for a self-hosted inference
// engine (ADR 0071). No membership and no tenant: an engine belongs to the deployment,
// and the question it answers — "was the GPU up, and was it serving anything" — has no
// owner to attribute it to.
//
// ⚠️ The ABSENCE of a row is meaningful. There is no separate heartbeat here (unlike
// UsageHourRow): the controller watches one engine per tick and cannot half-observe it,
// so a row IS the observation. No row = the hour was never watched, which the UI must
// draw as blank rather than as "stopped".
type EngineHourRow struct {
	EngineKey, Hour string
	EngineHourCounters
}

// EngineHourCounters is what one hour accumulates. Split out from the row so the
// controller's per-tick delta and the stored total are literally the same shape.
//
// ⚠️ ObservedSecs is the denominator and is STORED rather than reconstructed from
// Samples x interval. The controller's interval is not constant — it drops to 5 seconds
// while an engine is starting or warming — so samples x nominal interval overstates a
// busy hour and reports a running ratio above 100%.
//
// ⚠️ Running / Starting / Draining are disjoint, and only the first means "serving".
// Starting and Draining are both billing with nothing being answered: the cold start
// (165-197 s measured on a GPU box) and the several minutes Managed Instances keeps the
// EC2 instance after the task is gone (427-477 s measured). Summing them into RunningSecs
// would claim uptime that never served a request; dropping them would hide money.
// The json tags carry omitempty because these ride the API as-is.
type EngineHourCounters struct {
	Samples      int `json:"samples,omitempty"`
	ObservedSecs int `json:"observed_secs,omitempty"`
	RunningSecs  int `json:"running_secs,omitempty"`
	StartingSecs int `json:"starting_secs,omitempty"`
	DrainingSecs int `json:"draining_secs,omitempty"`
}

// UsageStore is showback usage (docs/log/roadmap.md P3-9). AddUsage accumulates
// workspace running-seconds into the (membership, day) bucket; ListUsage
// returns the per-day rows in [fromDay, toDay] (inclusive, YYYY-MM-DD), scoped
// to one tenant or, when tenantID=="", every tenant (super_admin).
type UsageStore interface {
	AddUsage(ctx context.Context, membershipID, tenantID, day string, secs int) error
	ListUsage(ctx context.Context, tenantID, fromDay, toDay string) ([]UsageRow, error)
	// AddUsageHour accumulates one sample into the (membership, hour) bucket: sums add,
	// peaks take the max. membershipID=="" writes the sampler heartbeat for the hour.
	AddUsageHour(ctx context.Context, membershipID, tenantID, hour string, d UsageHourCounters) error
	// ListUsageHourly returns the rows in [fromHour, toHour] (inclusive, YYYY-MM-DDTHH,
	// UTC). tenantID=="" spans every tenant; a tenant filter still returns the heartbeat
	// rows, because "was the CP even watching" is not a per-tenant fact.
	ListUsageHourly(ctx context.Context, tenantID, fromHour, toHour string) ([]UsageHourRow, error)
	// PruneUsageHourly drops rows strictly before beforeHour. usage_hourly is 24x the
	// rows of usage_daily and answers a question nobody asks about last spring, so it
	// has the retention usage_daily never needed.
	PruneUsageHourly(ctx context.Context, beforeHour string) error

	// AddEngineHour accumulates one controller tick into the (engine, hour) bucket
	// (ADR 0071). Everything here sums; there is no peak to take, because an engine's
	// desired count only ever moves between 0 and 1.
	AddEngineHour(ctx context.Context, engineKey, hour string, d EngineHourCounters) error
	// ListEngineHourly returns one engine's rows in [fromHour, toHour] (inclusive,
	// YYYY-MM-DDTHH, UTC). engineKey=="" spans every engine.
	ListEngineHourly(ctx context.Context, engineKey, fromHour, toHour string) ([]EngineHourRow, error)
	// PruneEngineHourly drops rows strictly before beforeHour, on the same retention as
	// PruneUsageHourly and from the same janitor.
	PruneEngineHourly(ctx context.Context, beforeHour string) error
}

// AddUsage accumulates workspace running-seconds into the (membership, day)
// showback bucket (docs/log/roadmap.md P3-9). Upsert += so repeated samples add up.
func (s *SQL) AddUsage(ctx context.Context, membershipID, tenantID, day string, secs int) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO usage_daily(membership_id, tenant_id, day, running_secs)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(membership_id, day) DO UPDATE SET running_secs = usage_daily.running_secs + excluded.running_secs`,
		membershipID, tenantID, day, secs)
	return err
}

// ListUsage returns per-day showback rows in [fromDay, toDay] (inclusive),
// enriched with tenant slug + member key/email via LEFT JOINs (a row survives
// even if its membership/identity was later removed). tenantID=="" spans all
// tenants (super_admin); otherwise it is scoped to that tenant.
func (s *SQL) ListUsage(ctx context.Context, tenantID, fromDay, toDay string) ([]UsageRow, error) {
	q := `SELECT u.tenant_id, COALESCE(t.slug,''), u.membership_id,
	             COALESCE(i.user_key,''), COALESCE(i.email,''), u.day, u.running_secs
	      FROM usage_daily u
	      LEFT JOIN tenant t ON t.id = u.tenant_id
	      LEFT JOIN membership m ON m.id = u.membership_id
	      LEFT JOIN identity i ON i.id = m.identity_id
	      WHERE u.day BETWEEN ? AND ?`
	args := []any{fromDay, toDay}
	if tenantID != "" {
		q += ` AND u.tenant_id=?`
		args = append(args, tenantID)
	}
	q += ` ORDER BY u.tenant_id, i.user_key, u.day`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageRow
	for rows.Next() {
		var u UsageRow
		if err := rows.Scan(&u.TenantID, &u.TenantSlug, &u.MembershipID,
			&u.UserKey, &u.Email, &u.Day, &u.RunningSecs); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// AddUsageHour accumulates one sample into the (membership, hour) bucket (docs/log/83).
//
// ⚠️ Sums accumulate, peaks do not. max_sessions/max_busy take a CASE-based maximum
// rather than SQLite's MAX(a,b) or Postgres's GREATEST(a,b): those are different
// spellings in the two dialects, and this store writes one SQL string for both.
func (s *SQL) AddUsageHour(ctx context.Context, membershipID, tenantID, hour string, d UsageHourCounters) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO usage_hourly(membership_id, tenant_id, hour, samples, running_secs,
		                          measured_secs, session_secs, busy_secs, max_sessions, max_busy)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(membership_id, hour) DO UPDATE SET
		   tenant_id     = excluded.tenant_id,
		   samples       = usage_hourly.samples       + excluded.samples,
		   running_secs  = usage_hourly.running_secs  + excluded.running_secs,
		   measured_secs = usage_hourly.measured_secs + excluded.measured_secs,
		   session_secs  = usage_hourly.session_secs  + excluded.session_secs,
		   busy_secs     = usage_hourly.busy_secs     + excluded.busy_secs,
		   max_sessions  = CASE WHEN excluded.max_sessions > usage_hourly.max_sessions
		                        THEN excluded.max_sessions ELSE usage_hourly.max_sessions END,
		   max_busy      = CASE WHEN excluded.max_busy > usage_hourly.max_busy
		                        THEN excluded.max_busy ELSE usage_hourly.max_busy END`,
		membershipID, tenantID, hour, d.Samples, d.RunningSecs,
		d.MeasuredSecs, d.SessionSecs, d.BusySecs, d.MaxSessions, d.MaxBusy)
	return err
}

// ListUsageHourly returns per-hour occupancy rows in [fromHour, toHour] (inclusive),
// with the same LEFT JOIN labelling as ListUsage so a deleted membership's history
// still surfaces.
//
// ⚠️ The tenant filter deliberately lets an EMPTY membership_id through. Those are the
// sampler heartbeats (tenant_id is empty on them by construction), and they are what
// tells the UI that an empty hour was observed rather than unrecorded. Filtering them
// out with the tenant would make every tenant-scoped heatmap blank.
func (s *SQL) ListUsageHourly(ctx context.Context, tenantID, fromHour, toHour string) ([]UsageHourRow, error) {
	q := `SELECT u.tenant_id, COALESCE(t.slug,''), u.membership_id,
	             COALESCE(i.user_key,''), COALESCE(i.email,''), u.hour,
	             u.samples, u.running_secs, u.measured_secs, u.session_secs, u.busy_secs,
	             u.max_sessions, u.max_busy
	      FROM usage_hourly u
	      LEFT JOIN tenant t ON t.id = u.tenant_id
	      LEFT JOIN membership m ON m.id = u.membership_id
	      LEFT JOIN identity i ON i.id = m.identity_id
	      WHERE u.hour BETWEEN ? AND ?`
	args := []any{fromHour, toHour}
	if tenantID != "" {
		q += ` AND (u.tenant_id=? OR u.membership_id='')`
		args = append(args, tenantID)
	}
	q += ` ORDER BY u.membership_id, u.hour`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageHourRow
	for rows.Next() {
		var u UsageHourRow
		if err := rows.Scan(&u.TenantID, &u.TenantSlug, &u.MembershipID,
			&u.UserKey, &u.Email, &u.Hour,
			&u.Samples, &u.RunningSecs, &u.MeasuredSecs, &u.SessionSecs, &u.BusySecs,
			&u.MaxSessions, &u.MaxBusy); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *SQL) PruneUsageHourly(ctx context.Context, beforeHour string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM usage_hourly WHERE hour < ?`, beforeHour)
	return err
}

// AddEngineHour accumulates one controller tick into the (engine, hour) bucket (ADR 0071).
//
// Everything sums, including observed_secs — that is the point of storing the denominator
// rather than deriving it from samples x interval, which the controller's variable interval
// (30 s idle, 5 s while starting) makes wrong in exactly the hours anybody looks at.
func (s *SQL) AddEngineHour(ctx context.Context, engineKey, hour string, d EngineHourCounters) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO engine_hourly(engine_key, hour, samples, observed_secs,
		                           running_secs, starting_secs, draining_secs)
		 VALUES(?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(engine_key, hour) DO UPDATE SET
		   samples       = engine_hourly.samples       + excluded.samples,
		   observed_secs = engine_hourly.observed_secs + excluded.observed_secs,
		   running_secs  = engine_hourly.running_secs  + excluded.running_secs,
		   starting_secs = engine_hourly.starting_secs + excluded.starting_secs,
		   draining_secs = engine_hourly.draining_secs + excluded.draining_secs`,
		engineKey, hour, d.Samples, d.ObservedSecs, d.RunningSecs, d.StartingSecs, d.DrainingSecs)
	return err
}

// ListEngineHourly returns one engine's hourly buckets in [fromHour, toHour] (inclusive).
// No JOIN: an engine is named by the stack's table, not by a row in this database, so
// there is nothing to label it with here.
func (s *SQL) ListEngineHourly(ctx context.Context, engineKey, fromHour, toHour string) ([]EngineHourRow, error) {
	q := `SELECT engine_key, hour, samples, observed_secs, running_secs, starting_secs, draining_secs
	      FROM engine_hourly WHERE hour BETWEEN ? AND ?`
	args := []any{fromHour, toHour}
	if engineKey != "" {
		q += ` AND engine_key=?`
		args = append(args, engineKey)
	}
	q += ` ORDER BY engine_key, hour`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EngineHourRow
	for rows.Next() {
		var e EngineHourRow
		if err := rows.Scan(&e.EngineKey, &e.Hour, &e.Samples, &e.ObservedSecs,
			&e.RunningSecs, &e.StartingSecs, &e.DrainingSecs); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *SQL) PruneEngineHourly(ctx context.Context, beforeHour string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM engine_hourly WHERE hour < ?`, beforeHour)
	return err
}
