package store

import (
	"context"
	"database/sql"
)

// CloudCostRow is one (day, membership, service) slice of the AWS invoice, as landed
// by the Cost Explorer poller (docs/log/67, ADR 0048). MembershipID=="" is the SHARED
// bucket — infrastructure that belongs to nobody. Amounts are integer micro-units of
// Currency; they are never converted to another currency and never divided among
// people (decision 4 / decision 6).
type CloudCostRow struct {
	Day, MembershipID, TenantID, Service string
	Unblended, Amortized                 int64
	Currency                             string
	Estimated                            bool
}

// CloudCostRoleRow is one (day, af-role, service) slice of the SHARED part of the invoice
// — the same money CloudCostRow holds with an empty MembershipID, cut by what it was FOR
// instead of by whose it was (ADR 0048 decision 15).
//
// Role=="" is not a gap: NAT, ALB, RDS, Route53 and tax carry no af-role and cannot, so it
// is the deployment's irreducible platform cost. The rows never overlap CloudCostRow's
// attributed ones, so the two must not be summed together.
type CloudCostRoleRow struct {
	Day, Role, Service   string
	Unblended, Amortized int64
	Currency             string
	Estimated            bool
}

// CloudCostTotal is one member's attributed spend over a window, enriched with the
// labels needed to name them. The labels are resolved at READ time by join, exactly
// like UsageRow: a membership that has been deleted leaves rows whose money is still
// real, and they surface with empty UserKey/Email rather than vanishing.
type CloudCostTotal struct {
	TenantSlug, MembershipID, UserKey, Email string
	Unblended, Amortized                     int64
	Currency                                 string
}

// CloudCostStore holds the invoice slices the Cost Explorer poller writes.
//
// PutCloudCost REPLACES a day wholesale rather than accumulating: Cost Explorer
// restates recent days (they arrive `Estimated` and move for ~24h), so the poller
// re-fetches a trailing window every run and the newest answer has to win. An
// accumulating write would double every re-fetch — the opposite of AddUsage next door,
// and the reason these two look similar but must not share code.
type CloudCostStore interface {
	// PutCloudCost replaces every row for the given days with rows. days lists exactly
	// the days the caller re-fetched, so a day that came back empty is emptied here too.
	PutCloudCost(ctx context.Context, days []string, rows []CloudCostRow) error
	// ListCloudCost returns rows in [fromDay,toDay]. tenantID=="" spans every tenant;
	// membershipID!="" narrows to one person (the member's own view).
	ListCloudCost(ctx context.Context, tenantID, membershipID, fromDay, toDay string) ([]CloudCostRow, error)
	// CloudCostTotals aggregates the same window per member, with labels joined in.
	CloudCostTotals(ctx context.Context, tenantID, fromDay, toDay string) ([]CloudCostTotal, error)
	// CloudCostDays reports which days have any row at all, so the API can tell
	// "nothing was spent" apart from "the poller has never covered this range" —
	// the difference matters because cost allocation cannot be backfilled.
	CloudCostDays(ctx context.Context) (first, last string, err error)

	// PutCloudCostByRole replaces the given days of the by-role cut. Same wholesale
	// replacement as PutCloudCost and for the same reason; a separate call because the
	// two come from two Cost Explorer requests and either can fail on its own.
	PutCloudCostByRole(ctx context.Context, days []string, rows []CloudCostRoleRow) error
	// ListCloudCostByRole returns the by-role rows in [fromDay,toDay]. There is no
	// tenant or membership parameter on purpose: every row here is shared, which is
	// super_admin-only information (ADR 0048 decision 4).
	ListCloudCostByRole(ctx context.Context, fromDay, toDay string) ([]CloudCostRoleRow, error)
}

// --- cloud cost (docs/log/67, ADR 0048) --------------------------------------------

// PutCloudCost replaces the given days wholesale. Cost Explorer restates recent days
// (they arrive `Estimated` and keep moving for about a day), so the poller re-fetches a
// trailing window every run and the latest answer must WIN, not accumulate. Deleting the
// days first is also what makes a day that came back empty actually become empty —
// otherwise a resource that lost its tag would leave its last attributed row frozen in
// place forever, and the invoice would never stop blaming that person.
func (s *SQL) PutCloudCost(ctx context.Context, days []string, rows []CloudCostRow) error {
	if len(days) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, d := range days {
		if _, err = tx.ExecContext(ctx, `DELETE FROM cloud_cost_daily WHERE day=?`, d); err != nil {
			return err
		}
	}
	now := NowTS()
	for _, r := range rows {
		est := 0
		if r.Estimated {
			est = 1
		}
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO cloud_cost_daily(day, membership_id, tenant_id, service,
			   unblended, amortized, currency, estimated, updated_at)
			 VALUES(?,?,?,?,?,?,?,?,?)
			 ON CONFLICT(day, membership_id, service) DO UPDATE SET
			   tenant_id=excluded.tenant_id, unblended=excluded.unblended,
			   amortized=excluded.amortized, currency=excluded.currency,
			   estimated=excluded.estimated, updated_at=excluded.updated_at`,
			r.Day, r.MembershipID, r.TenantID, r.Service,
			r.Unblended, r.Amortized, r.Currency, est, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListCloudCost returns the raw per-(day, member, service) rows in the window.
// membershipID != "" is the member's own view; tenantID != "" scopes to one tenant.
//
// The shared bucket (membership_id=”) has no tenant, so a tenant-scoped query must
// NOT return it — a tenant_admin seeing the deployment's ALB/RDS bill would be reading
// outside their tenant (ADR 0048 decision 4). It falls out of the WHERE naturally, and that
// is deliberate rather than accidental.
func (s *SQL) ListCloudCost(ctx context.Context, tenantID, membershipID, fromDay, toDay string) ([]CloudCostRow, error) {
	q := `SELECT day, membership_id, tenant_id, service, unblended, amortized, currency, estimated
	      FROM cloud_cost_daily WHERE day BETWEEN ? AND ?`
	args := []any{fromDay, toDay}
	if tenantID != "" {
		q += ` AND tenant_id=?`
		args = append(args, tenantID)
	}
	if membershipID != "" {
		q += ` AND membership_id=?`
		args = append(args, membershipID)
	}
	q += ` ORDER BY day, membership_id, service`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CloudCostRow
	for rows.Next() {
		var r CloudCostRow
		var est int
		if err := rows.Scan(&r.Day, &r.MembershipID, &r.TenantID, &r.Service,
			&r.Unblended, &r.Amortized, &r.Currency, &est); err != nil {
			return nil, err
		}
		r.Estimated = est != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// CloudCostTotals sums the window per member, joining the labels in at read time (a
// deleted membership keeps its spend and surfaces with empty key/email, like ListUsage).
// The shared bucket is included as a row with an empty MembershipID; the API decides
// who is allowed to see it.
func (s *SQL) CloudCostTotals(ctx context.Context, tenantID, fromDay, toDay string) ([]CloudCostTotal, error) {
	q := `SELECT COALESCE(t.slug,''), c.membership_id, COALESCE(i.user_key,''), COALESCE(i.email,''),
	             SUM(c.unblended), SUM(c.amortized), COALESCE(MAX(c.currency),'')
	      FROM cloud_cost_daily c
	      LEFT JOIN tenant t ON t.id = c.tenant_id
	      LEFT JOIN membership m ON m.id = c.membership_id
	      LEFT JOIN identity i ON i.id = m.identity_id
	      WHERE c.day BETWEEN ? AND ?`
	args := []any{fromDay, toDay}
	if tenantID != "" {
		q += ` AND c.tenant_id=?`
		args = append(args, tenantID)
	}
	q += ` GROUP BY t.slug, c.membership_id, i.user_key, i.email ORDER BY SUM(c.unblended) DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CloudCostTotal
	for rows.Next() {
		var c CloudCostTotal
		if err := rows.Scan(&c.TenantSlug, &c.MembershipID, &c.UserKey, &c.Email,
			&c.Unblended, &c.Amortized, &c.Currency); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PutCloudCostByRole replaces the given days of the by-role cut, wholesale, for exactly
// the reasons PutCloudCost does: Cost Explorer restates recent days, and a role that stops
// being billed has to be able to reach zero rather than freeze at its last value.
//
// It is a second transaction rather than part of the first because the two cuts come from
// two Cost Explorer requests: if the by-role one fails, the by-member answer that already
// arrived is still worth storing.
func (s *SQL) PutCloudCostByRole(ctx context.Context, days []string, rows []CloudCostRoleRow) error {
	if len(days) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, d := range days {
		if _, err = tx.ExecContext(ctx, `DELETE FROM cloud_cost_role_daily WHERE day=?`, d); err != nil {
			return err
		}
	}
	now := NowTS()
	for _, r := range rows {
		est := 0
		if r.Estimated {
			est = 1
		}
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO cloud_cost_role_daily(day, role, service,
			   unblended, amortized, currency, estimated, updated_at)
			 VALUES(?,?,?,?,?,?,?,?)
			 ON CONFLICT(day, role, service) DO UPDATE SET
			   unblended=excluded.unblended, amortized=excluded.amortized,
			   currency=excluded.currency, estimated=excluded.estimated,
			   updated_at=excluded.updated_at`,
			r.Day, r.Role, r.Service,
			r.Unblended, r.Amortized, r.Currency, est, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListCloudCostByRole returns the raw per-(day, role, service) rows in the window. No
// tenant or membership filter exists because every row is shared cost — the handler's job
// is to refuse the whole thing to anyone below super_admin, not to narrow it.
func (s *SQL) ListCloudCostByRole(ctx context.Context, fromDay, toDay string) ([]CloudCostRoleRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT day, role, service, unblended, amortized, currency, estimated
		 FROM cloud_cost_role_daily WHERE day BETWEEN ? AND ?
		 ORDER BY day, role, service`, fromDay, toDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CloudCostRoleRow
	for rows.Next() {
		var r CloudCostRoleRow
		var est int
		if err := rows.Scan(&r.Day, &r.Role, &r.Service,
			&r.Unblended, &r.Amortized, &r.Currency, &est); err != nil {
			return nil, err
		}
		r.Estimated = est != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// CloudCostDays is the coverage window that actually exists. It is what lets the API say
// "cost allocation was not switched on before this date" instead of drawing an honest-
// looking zero — and that distinction is permanent, because activation is not
// retroactive (docs/log/67 §67.5).
func (s *SQL) CloudCostDays(ctx context.Context) (string, string, error) {
	var first, last sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT MIN(day), MAX(day) FROM cloud_cost_daily`).Scan(&first, &last)
	if err != nil {
		return "", "", err
	}
	return first.String, last.String, nil
}
