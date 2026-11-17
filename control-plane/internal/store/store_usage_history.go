package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/usagehist"
)

// Launch is one container launch, as the CP recorded it (ADR 0139 decision 3). Times are unix
// seconds. A launch is the unit running time is read from, and the box it names is the one a
// measured minute is attributed to.
type Launch struct {
	ID, WorkspaceID, MembershipID, TenantID string
	StartedAt                               int64
	// StoppedAt is 0 while the launch is open.
	StoppedAt int64
	// LastSeenAt is the last sampler tick that saw the launch running, where an unrecorded
	// stop is closed.
	LastSeenAt int64
	// RanThrough is how far this launch's seconds are already in member_usage_daily.
	RanThrough int64
	// InstanceType is the box the runtime placed the launch on, ClassID its machine class and
	// Override what chose it ("" = the member's own setting). VCPU and BaselinePct are what the
	// ladder declared for the rung, 0 where it declared nothing.
	InstanceType, ClassID, Override string
	VCPU, BaselinePct               int
}

// End is where a launch's seconds stop: its stop, else the last time it was seen running.
func (l Launch) End() int64 {
	if l.StoppedAt > 0 {
		return l.StoppedAt
	}
	return l.LastSeenAt
}

// UsageWindow is one Agent minute (mempressure.Window): the figures it could measure, nil for
// the ones it could not.
type UsageWindow struct {
	T                                      int64
	LID                                    string
	CPUPct, Some10Max, Full60, RefaultRate *float64
	WorkingSet                             *uint64
}

// UsageDay is one member_usage_daily row. The measured-minute counters say how many of the
// running minutes each signal actually has; a quantile, a max or a count is meaningful only
// against its own counter, and the figures that need a declaration the rung did not make are
// nil.
type UsageDay struct {
	TenantID, MembershipID, Day        string
	InstanceType, ClassID, Override    string
	VCPU, BaselinePct                  int
	RunningSecs                        int
	CPUMeasuredMin, WSMeasuredMin      int
	PSIMeasuredMin, RefaultMeasuredMin int
	CPUHist, WSHist, RefaultHist       usagehist.Hist
	CPUMax                             *float64
	WSMax                              *uint64
	PSIFullMax                         *float64
	PSIWarnMin                         int
	BurstMin                           *int
}

// UsageDayLabelled adds the labels a read joins in, the way UsageRow does.
type UsageDayLabelled struct {
	UsageDay
	TenantSlug, UserKey, Email string
}

// ErrUsageCursorMoved is FoldWindows losing a race: another pull folded the same minutes first.
// Nothing was written and the caller drops what it fetched.
var ErrUsageCursorMoved = errors.New("usage pull cursor moved")

// UsageHistoryStore is the per-member usage history (ADR 0139 decision 3).
type UsageHistoryStore interface {
	// BeginLaunch records a launch. Any launch of the membership still open is closed first, at
	// its last-seen time: a member has one container, so a new launch proves the old one is
	// over, and it ran no longer than it was seen.
	BeginLaunch(ctx context.Context, l Launch) error
	// OpenLaunch is the membership's open launch, if any.
	OpenLaunch(ctx context.Context, membershipID string) (Launch, bool, error)
	// TouchLaunch records that the launch was seen running at the given unix second.
	TouchLaunch(ctx context.Context, id string, at int64) error
	// EndOpenLaunch closes the membership's open launch at `at` (clamped to between its last
	// seen time and now is the caller's job) and reports whether one was open.
	EndOpenLaunch(ctx context.Context, membershipID string, at int64) (Launch, bool, error)
	// FoldRunning adds the seconds of every launch of the membership that have not been counted
	// yet to member_usage_daily, split at UTC midnights, and advances each launch's cursor. It
	// can be called as often as wanted: seconds are counted once.
	FoldRunning(ctx context.Context, membershipID string) error
	// GetLaunch reads one launch.
	GetLaunch(ctx context.Context, id string) (Launch, bool, error)

	// UsagePullCursor is how far the membership's Agent series has been folded (0 = never).
	UsagePullCursor(ctx context.Context, membershipID string) (int64, error)
	// FoldWindows folds the Agent's windows that lie after `from` and up to `to` into the day
	// rows of the launches they carry, and moves the cursor from `from` to `to`, all in one
	// transaction. ErrUsageCursorMoved when the cursor is no longer `from`. A window whose
	// launch is unknown here is dropped: there is no box to put it on.
	FoldWindows(ctx context.Context, membershipID string, from, to int64, ws []UsageWindow) error

	// ListUsageDays returns day rows in [fromDay, toDay], one tenant (tenantID != "") or one
	// membership (membershipID != ""), oldest first.
	ListUsageDays(ctx context.Context, tenantID, membershipID, fromDay, toDay string) ([]UsageDayLabelled, error)
	// PruneUsageHistory drops day rows before beforeDay and launches that ended before the
	// matching unix second.
	PruneUsageHistory(ctx context.Context, beforeDay string, beforeUnix int64) error
}

const launchCols = `id, workspace_id, membership_id, tenant_id, started_at, stopped_at, last_seen_at,
	ran_through, instance_type, class_id, override, vcpu, baseline_pct`

func scanLaunch(sc interface{ Scan(...any) error }) (Launch, error) {
	var l Launch
	var stopped sql.NullInt64
	err := sc.Scan(&l.ID, &l.WorkspaceID, &l.MembershipID, &l.TenantID, &l.StartedAt, &stopped,
		&l.LastSeenAt, &l.RanThrough, &l.InstanceType, &l.ClassID, &l.Override, &l.VCPU, &l.BaselinePct)
	l.StoppedAt = stopped.Int64
	return l, err
}

func (s *SQL) BeginLaunch(ctx context.Context, l Launch) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The old launch ends where it was last seen. Its seconds up to there are folded by the
	// caller's FoldRunning, which reads the closed row.
	if _, err := tx.ExecContext(ctx,
		`UPDATE workspace_launch SET stopped_at = last_seen_at
		 WHERE membership_id=? AND stopped_at IS NULL`, l.MembershipID); err != nil {
		return err
	}
	if l.LastSeenAt == 0 {
		l.LastSeenAt = l.StartedAt
	}
	if l.RanThrough == 0 {
		l.RanThrough = l.StartedAt
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO workspace_launch(`+launchCols+`) VALUES(?,?,?,?,?,NULL,?,?,?,?,?,?,?)`,
		l.ID, l.WorkspaceID, l.MembershipID, l.TenantID, l.StartedAt, l.LastSeenAt, l.RanThrough,
		l.InstanceType, l.ClassID, l.Override, l.VCPU, l.BaselinePct); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQL) OpenLaunch(ctx context.Context, membershipID string) (Launch, bool, error) {
	l, err := scanLaunch(s.db.QueryRowContext(ctx,
		`SELECT `+launchCols+` FROM workspace_launch
		 WHERE membership_id=? AND stopped_at IS NULL ORDER BY started_at DESC LIMIT 1`, membershipID))
	if errors.Is(err, sql.ErrNoRows) {
		return Launch{}, false, nil
	}
	return l, err == nil, err
}

func (s *SQL) GetLaunch(ctx context.Context, id string) (Launch, bool, error) {
	l, err := scanLaunch(s.db.QueryRowContext(ctx,
		`SELECT `+launchCols+` FROM workspace_launch WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Launch{}, false, nil
	}
	return l, err == nil, err
}

func (s *SQL) TouchLaunch(ctx context.Context, id string, at int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE workspace_launch SET last_seen_at=? WHERE id=? AND stopped_at IS NULL AND last_seen_at < ?`,
		at, id, at)
	return err
}

func (s *SQL) EndOpenLaunch(ctx context.Context, membershipID string, at int64) (Launch, bool, error) {
	l, ok, err := s.OpenLaunch(ctx, membershipID)
	if err != nil || !ok {
		return Launch{}, false, err
	}
	if at < l.LastSeenAt {
		at = l.LastSeenAt // never end before it was seen running
	}
	if at < l.StartedAt {
		at = l.StartedAt
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE workspace_launch SET stopped_at=? WHERE id=? AND stopped_at IS NULL`, at, l.ID)
	if err != nil {
		return Launch{}, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Launch{}, false, nil // another replica closed it first
	}
	l.StoppedAt = at
	return l, true, nil
}

// usageDayKey is the identity of a member_usage_daily row.
type usageDayKey struct{ day, instanceType, classID, override string }

func dayOf(unix int64) string { return time.Unix(unix, 0).UTC().Format("2006-01-02") }

func ensureUsageDay(ctx context.Context, tx *sqlTx, l Launch, k usageDayKey) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO member_usage_daily(membership_id, day, instance_type, class_id, override, tenant_id, vcpu, baseline_pct)
		 VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(membership_id, day, instance_type, class_id, override) DO NOTHING`,
		l.MembershipID, k.day, k.instanceType, k.classID, k.override, l.TenantID, l.VCPU, l.BaselinePct)
	return err
}

func (s *SQL) FoldRunning(ctx context.Context, membershipID string) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+launchCols+` FROM workspace_launch WHERE membership_id=?
		 AND ran_through < COALESCE(stopped_at, last_seen_at)`, membershipID)
	if err != nil {
		return err
	}
	var todo []Launch
	for rows.Next() {
		l, err := scanLaunch(rows)
		if err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, l)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, l := range todo {
		if err := s.foldLaunchRunning(ctx, l); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQL) foldLaunchRunning(ctx context.Context, l Launch) error {
	end := l.End()
	if end <= l.RanThrough {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The guard is on the launch row itself: two folds of the same launch (a tick and a Stop)
	// cannot both advance it from the same point, so a second one writes nothing.
	res, err := tx.ExecContext(ctx,
		`UPDATE workspace_launch SET ran_through=? WHERE id=? AND ran_through=?`, end, l.ID, l.RanThrough)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	for from := l.RanThrough; from < end; {
		next := time.Unix(from, 0).UTC().Truncate(24 * time.Hour).Add(24 * time.Hour).Unix()
		if next > end {
			next = end
		}
		k := usageDayKey{dayOf(from), l.InstanceType, l.ClassID, l.Override}
		if err := ensureUsageDay(ctx, tx, l, k); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE member_usage_daily SET running_secs = running_secs + ?
			 WHERE membership_id=? AND day=? AND instance_type=? AND class_id=? AND override=?`,
			next-from, l.MembershipID, k.day, k.instanceType, k.classID, k.override); err != nil {
			return err
		}
		from = next
	}
	return tx.Commit()
}

func (s *SQL) UsagePullCursor(ctx context.Context, membershipID string) (int64, error) {
	var c int64
	err := s.db.QueryRowContext(ctx, `SELECT cursor_t FROM usage_pull_cursor WHERE membership_id=?`, membershipID).Scan(&c)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return c, err
}

// usageDayAcc is one row's worth of windows, merged into the stored row in one read-modify-write.
type usageDayAcc struct {
	l                                  Launch
	cpuN, wsN, psiN, refN, warn, burst int
	cpu, ws, ref                       usagehist.Hist
	cpuMax, psiMax                     *float64
	wsMax                              *uint64
	burstDeclared                      bool
}

// psiWarnSomeAvg10 is the PSI `some` avg10 at or above which a minute counts as above the
// warning threshold. It is ADR 0120 decision 2's stage-1 value, which that ADR calls
// provisional, applied to the window's highest sample.
const psiWarnSomeAvg10 = 10

func (s *SQL) FoldWindows(ctx context.Context, membershipID string, from, to int64, ws []UsageWindow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The cursor is claimed first. On Postgres the UPDATE holds the row until commit, so a
	// concurrent fold of the same membership waits here and then finds the cursor moved.
	var res sql.Result
	if from == 0 {
		res, err = tx.ExecContext(ctx,
			`INSERT INTO usage_pull_cursor(membership_id, cursor_t) VALUES(?,?) ON CONFLICT(membership_id) DO NOTHING`,
			membershipID, to)
	} else {
		res, err = tx.ExecContext(ctx,
			`UPDATE usage_pull_cursor SET cursor_t=? WHERE membership_id=? AND cursor_t=?`, to, membershipID, from)
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUsageCursorMoved
	}
	launches := map[string]*Launch{}
	accs := map[usageDayKey]*usageDayAcc{}
	for _, w := range ws {
		l, seen := launches[w.LID]
		if !seen {
			var found Launch
			var ok bool
			found, err = scanLaunch(tx.QueryRowContext(ctx, `SELECT `+launchCols+` FROM workspace_launch WHERE id=?`, w.LID))
			if err == nil {
				ok = true
			} else if errors.Is(err, sql.ErrNoRows) {
				err = nil
			} else {
				return err
			}
			if ok && found.MembershipID == membershipID {
				l = &found
			}
			launches[w.LID] = l
		}
		if l == nil {
			continue
		}
		k := usageDayKey{dayOf(w.T), l.InstanceType, l.ClassID, l.Override}
		a := accs[k]
		if a == nil {
			a = &usageDayAcc{l: *l, burstDeclared: l.VCPU > 0 && l.BaselinePct > 0}
			accs[k] = a
		}
		a.add(w)
	}
	for k, a := range accs {
		if err := ensureUsageDay(ctx, tx, a.l, k); err != nil {
			return err
		}
		if err := a.merge(ctx, tx, k); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *usageDayAcc) add(w UsageWindow) {
	if w.CPUPct != nil {
		a.cpuN++
		a.cpu.Add(usagehist.CPU, *w.CPUPct)
		if a.cpuMax == nil || *w.CPUPct > *a.cpuMax {
			v := *w.CPUPct
			a.cpuMax = &v
		}
		// The baseline is a share of the whole box, the Agent's figure a share of one core.
		if a.burstDeclared && *w.CPUPct/float64(a.l.VCPU) > float64(a.l.BaselinePct) {
			a.burst++
		}
	}
	if w.WorkingSet != nil {
		a.wsN++
		a.ws.Add(usagehist.WorkingSet, float64(*w.WorkingSet))
		if a.wsMax == nil || *w.WorkingSet > *a.wsMax {
			v := *w.WorkingSet
			a.wsMax = &v
		}
	}
	// Pressure counts only when both of its figures were read.
	if w.Some10Max != nil && w.Full60 != nil {
		a.psiN++
		if a.psiMax == nil || *w.Full60 > *a.psiMax {
			v := *w.Full60
			a.psiMax = &v
		}
		if *w.Some10Max >= psiWarnSomeAvg10 {
			a.warn++
		}
	}
	if w.RefaultRate != nil {
		a.refN++
		a.ref.Add(usagehist.Refault, *w.RefaultRate)
	}
}

func (a *usageDayAcc) merge(ctx context.Context, tx *sqlTx, k usageDayKey) error {
	var cpuH, wsH, refH string
	var cpuMax, psiMax sql.NullFloat64
	var wsMax, burst sql.NullInt64
	var cpuN, wsN, psiN, refN, warn int
	where := ` WHERE membership_id=? AND day=? AND instance_type=? AND class_id=? AND override=?`
	args := []any{a.l.MembershipID, k.day, k.instanceType, k.classID, k.override}
	if err := tx.QueryRowContext(ctx,
		`SELECT cpu_hist, ws_hist, refault_hist, cpu_max, ws_max, psi_full_max, burst_min,
		        cpu_measured_min, ws_measured_min, psi_measured_min, refault_measured_min, psi_warn_min
		 FROM member_usage_daily`+where, args...).Scan(&cpuH, &wsH, &refH, &cpuMax, &wsMax, &psiMax, &burst,
		&cpuN, &wsN, &psiN, &refN, &warn); err != nil {
		return err
	}
	cpu, ws, ref := usagehist.Decode(cpuH), usagehist.Decode(wsH), usagehist.Decode(refH)
	cpu.Merge(a.cpu)
	ws.Merge(a.ws)
	ref.Merge(a.ref)
	if a.cpuMax != nil && (!cpuMax.Valid || *a.cpuMax > cpuMax.Float64) {
		cpuMax = sql.NullFloat64{Float64: *a.cpuMax, Valid: true}
	}
	if a.psiMax != nil && (!psiMax.Valid || *a.psiMax > psiMax.Float64) {
		psiMax = sql.NullFloat64{Float64: *a.psiMax, Valid: true}
	}
	if a.wsMax != nil && (!wsMax.Valid || int64(*a.wsMax) > wsMax.Int64) {
		wsMax = sql.NullInt64{Int64: int64(*a.wsMax), Valid: true}
	}
	// NULL until the rung declares a baseline AND a CPU minute was measured against it: zero
	// minutes above a baseline nobody declared is not an answer.
	if a.burstDeclared && a.cpuN > 0 {
		burst = sql.NullInt64{Int64: burst.Int64 + int64(a.burst), Valid: true}
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE member_usage_daily SET cpu_hist=?, ws_hist=?, refault_hist=?, cpu_max=?, ws_max=?, psi_full_max=?,
		   burst_min=?, cpu_measured_min=?, ws_measured_min=?, psi_measured_min=?, refault_measured_min=?, psi_warn_min=?`+where,
		append([]any{cpu.Encode(), ws.Encode(), ref.Encode(), nullFloat(cpuMax), nullInt64(wsMax), nullFloat(psiMax),
			nullInt64(burst), cpuN + a.cpuN, wsN + a.wsN, psiN + a.psiN, refN + a.refN, warn + a.warn}, args...)...)
	return err
}

func nullFloat(v sql.NullFloat64) any {
	if !v.Valid {
		return nil
	}
	return v.Float64
}

func nullInt64(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

func (s *SQL) ListUsageDays(ctx context.Context, tenantID, membershipID, fromDay, toDay string) ([]UsageDayLabelled, error) {
	q := `SELECT u.tenant_id, COALESCE(t.slug,''), u.membership_id, COALESCE(i.user_key,''), COALESCE(i.email,''),
	             u.day, u.instance_type, u.class_id, u.override, u.vcpu, u.baseline_pct, u.running_secs,
	             u.cpu_measured_min, u.ws_measured_min, u.psi_measured_min, u.refault_measured_min,
	             u.cpu_hist, u.ws_hist, u.refault_hist, u.cpu_max, u.ws_max, u.psi_full_max, u.psi_warn_min, u.burst_min
	      FROM member_usage_daily u
	      LEFT JOIN tenant t ON t.id = u.tenant_id
	      LEFT JOIN membership m ON m.id = u.membership_id
	      LEFT JOIN identity i ON i.id = m.identity_id
	      WHERE u.day BETWEEN ? AND ?`
	args := []any{fromDay, toDay}
	if tenantID != "" {
		q += ` AND u.tenant_id=?`
		args = append(args, tenantID)
	}
	if membershipID != "" {
		q += ` AND u.membership_id=?`
		args = append(args, membershipID)
	}
	q += ` ORDER BY u.tenant_id, i.user_key, u.membership_id, u.day, u.instance_type, u.class_id, u.override`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageDayLabelled
	for rows.Next() {
		var r UsageDayLabelled
		var cpuH, wsH, refH string
		var cpuMax, psiMax sql.NullFloat64
		var wsMax, burst sql.NullInt64
		if err := rows.Scan(&r.TenantID, &r.TenantSlug, &r.MembershipID, &r.UserKey, &r.Email,
			&r.Day, &r.InstanceType, &r.ClassID, &r.Override, &r.VCPU, &r.BaselinePct, &r.RunningSecs,
			&r.CPUMeasuredMin, &r.WSMeasuredMin, &r.PSIMeasuredMin, &r.RefaultMeasuredMin,
			&cpuH, &wsH, &refH, &cpuMax, &wsMax, &psiMax, &r.PSIWarnMin, &burst); err != nil {
			return nil, err
		}
		r.CPUHist, r.WSHist, r.RefaultHist = usagehist.Decode(cpuH), usagehist.Decode(wsH), usagehist.Decode(refH)
		if cpuMax.Valid {
			v := cpuMax.Float64
			r.CPUMax = &v
		}
		if psiMax.Valid {
			v := psiMax.Float64
			r.PSIFullMax = &v
		}
		if wsMax.Valid {
			v := uint64(wsMax.Int64)
			r.WSMax = &v
		}
		if burst.Valid {
			v := int(burst.Int64)
			r.BurstMin = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQL) PruneUsageHistory(ctx context.Context, beforeDay string, beforeUnix int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM member_usage_daily WHERE day < ?`, beforeDay); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM workspace_launch WHERE stopped_at IS NOT NULL AND stopped_at < ? AND ran_through >= stopped_at`, beforeUnix)
	return err
}
