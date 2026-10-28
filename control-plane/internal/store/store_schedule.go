package store

import (
	"context"
	"database/sql"
)

// Schedule is one operator-authored scheduled task (docs/log/38 + ADR0021). It lives
// in the CP DB because the CP is the only thing alive while the workspace is stopped.
// SpecKind is "cron" (Spec is a 5-field cron expr), "interval" (Spec is whole
// seconds) or "once" (Spec is an RFC3339 absolute instant). Spec is the evaluated
// source of truth; TZ is the IANA zone cron is evaluated in (DST included); SpecLabel
// keeps the operator's original natural-language phrasing for display only. NextRun/
// LastRun are UTC RFC3339 fire-ledger stamps (NextRun="" means never/disabled).
type Schedule struct {
	ID, MembershipID, TenantID string
	OwnerConv                  string // operator conversation id — the report_to target (docs/log/30)
	SpecKind, Spec, SpecLabel  string
	TZ                         string
	WakePolicy                 string // wake (default) | skip | catch_up
	SessionMode, ReuseTarget   string // new (default) | reuse; ReuseTarget = pinned session name for reuse (P6)
	AgentKind, Model           string
	Repo, Worktree             string
	NewBranch                  bool
	Prompt                     string
	OverlapPolicy              string // skip (default) | queue | restart (reuse only, P6)
	// Report opts a fire into the docs/log/30 completion report: true passes OwnerConv as the
	// session's report_to so the result comes back to the operator/assistant conversation.
	// Default false = fire silently (run history / failure notifications still surface).
	Report bool
	// StopAfterRun folds the fire's session away once it has finished the prompt
	// (docs/log/85): the Agent is armed to stop the session at the end of that turn, after
	// any report it owes has gone out. Default false = leave it running, which is the
	// behaviour every existing schedule was written against.
	//
	// What it saves is memory, not money: an unattended fire otherwise leaves its session
	// alive for the rest of the night, holding a CLI process on a shared host. It does NOT
	// bring the workspace's own stop forward — a finished session is idle, and idle was
	// never what kept the workspace awake (holdsWorkspace is machineBusy only). Ignored in
	// session_mode=assistant, which drives a conversation and never holds a session.
	StopAfterRun bool
	// DeliverTo is where a fire's result goes when Report is on (issue #1560): a
	// comma-separated subset of operator, notifications, discord and slack. Empty means
	// operator alone, the meaning Report had before the column existed.
	DeliverTo string
	// Silent lets a run whose final answer is exactly the sentinel ([SILENT]) deliver
	// nothing: the run is recorded as fired_silent instead. Failures are never silent.
	Silent               bool
	Enabled              bool
	NextRun, LastRun     string
	LastStatus           string
	CreatedAt, UpdatedAt string
	// Reuse ledger (P6, docs/log/38): the current long-lived session the scheduler drives in
	// session_mode=reuse, when it started, and how many fires it has taken since the last
	// rotation. Rotation is a JSON blob of triggers (every_runs/after/calendar). MissingTargetPolicy
	// governs a pinned reuse whose target session vanished (recreate default | fail).
	ReuseSession, ReuseStartedAt string
	ReuseRunCount                int
	Rotation                     string
	MissingTargetPolicy          string
	// ManualFirePending is set by run-now and read+cleared by the scheduler on the next
	// fire to tag that run as manual (docs/log/38). Transient — not part of the schedule DTO.
	ManualFirePending bool
	// HeldByRemoval: the scheduler paused this row because its owner had been removed
	// (issue #1087); a re-invite resumes it. Cleared by any change the owner makes. Not
	// part of the schedule DTO.
	HeldByRemoval bool
}

// ScheduleRun is one fire-attempt history row (docs/log/38 P3 get_schedule_runs).
// Status mirrors the schedule's last_status token; FiredAt is UTC RFC3339. Session is
// the session the fire drove — the newly created session (session_mode=new) or the
// long-lived reuse target (session_mode=reuse) — so the Console history can open it. It
// is empty for soft skips that ran nothing (skipped_* / error before a session existed).
// Trigger is how the fire was initiated: "manual" (a run-now) or "scheduled" (an automatic
// wall-clock fire, the default), so the history can tell an on-demand run from a timed one.
type ScheduleRun struct {
	ID, ScheduleID, MembershipID string
	FiredAt, Status, Detail      string
	Session                      string
	Trigger                      string
	// Slot is the slot the run fired for (RFC 3339 UTC), empty on rows written before it was
	// recorded. MarkScheduleRunNotExecuted names a run by it.
	Slot string
}

// ScheduleStore is the scheduled-execution definition store (docs/log/38 + ADR0021).
// The CP-resident scheduler (scheduler.go) reads ListDueSchedules on every tick and
// stamps the fire ledger via RecordScheduleFire; the operator MCP (P3) drives the
// membership-scoped CRUD. Mutations that a member issues carry membership_id in the
// WHERE so one member never touches another's rows.
type ScheduleStore interface {
	CreateSchedule(ctx context.Context, s Schedule) error
	GetSchedule(ctx context.Context, id string) (Schedule, bool, error)
	ListSchedules(ctx context.Context, membershipID string) ([]Schedule, error)
	// ListDueSchedules returns enabled schedules whose next_run is non-empty and at
	// or before nowRFC (an RFC3339 UTC cutoff; RFC3339 sorts chronologically).
	ListDueSchedules(ctx context.Context, nowRFC string) ([]Schedule, error)
	UpdateSchedule(ctx context.Context, s Schedule) error
	SetScheduleEnabled(ctx context.Context, id, membershipID string, enabled bool, nextRun, updatedAt string) error
	// MarkManualFirePending is run-now: it sets next_run so the ticker fires immediately and
	// flags manual_fire_pending so the resulting run is tagged as a manual fire (docs/log/38).
	MarkManualFirePending(ctx context.Context, id, membershipID, nextRun, updatedAt string) error
	DeleteSchedule(ctx context.Context, id, membershipID string) error
	// RecordScheduleFire stamps the ledger after a fire attempt: last_run/last_status
	// and the recomputed next_run, disabling the row (enabled=0) when next_run is ""
	// (a spent "once"). The scheduler is the only caller, so no membership scoping.
	RecordScheduleFire(ctx context.Context, id, lastRun, lastStatus, nextRun string, enabled bool, updatedAt string) error
	// HoldScheduleForRemoval / ResumeScheduleHeldByRemoval are the two halves of member
	// removal (issue #1087): the scheduler pauses a slot that came due while the owner
	// was inactive, and a re-invite resumes it. Both are conditional single statements —
	// the hold only while the membership is not active, the resume only while the row is
	// still held — and every owner-side write clears the mark.
	HoldScheduleForRemoval(ctx context.Context, id, slot, lastRun, lastStatus, updatedAt string) (held bool, err error)
	ResumeScheduleHeldByRemoval(ctx context.Context, id, membershipID, nextRun, updatedAt string) (resumed bool, err error)
	// SetScheduleReuse persists the reuse ledger (P6): the current long-lived session,
	// when it started, and the fire count since the last rotation. Only reuse schedules
	// use it; the firer calls it, so no membership scoping.
	SetScheduleReuse(ctx context.Context, id, reuseSession, reuseStartedAt string, runCount int, updatedAt string) error
	// AppendScheduleRun inserts a run-history row and trims that schedule's history to
	// the keepN most recent rows so a frequent schedule cannot grow it without bound.
	AppendScheduleRun(ctx context.Context, run ScheduleRun, keepN int) error
	// ListScheduleRuns returns a schedule's most-recent runs (newest first), scoped by
	// membership so a member only sees their own schedule's history.
	ListScheduleRuns(ctx context.Context, scheduleID, membershipID string, limit int) ([]ScheduleRun, error)
	// MarkScheduleRunNotExecuted rewrites the status and detail of the run that fired into
	// session for slot, when it is still recorded as fired. found is false when no run has that
	// session and slot (trimmed, recorded before slots were, or never fired). changed is false
	// when the run is found but no longer fired: a repeated report, or a run that failed.
	MarkScheduleRunNotExecuted(ctx context.Context, scheduleID, membershipID, session, slot, status, detail string) (found, changed bool, err error)
	// MarkScheduleRunSilent records that the run that fired into session for slot answered
	// with the silent sentinel (issue #1560): its status becomes ScheduleStatusFiredSilent.
	// Only a run still recorded as plainly fired changes, so a failure is never overwritten.
	MarkScheduleRunSilent(ctx context.Context, scheduleID, membershipID, session, slot string) (found, changed bool, err error)
}

// ScheduleStatusFiredSilent is the run status of a fire whose session answered with the
// silent sentinel and so delivered nothing (issue #1560). It keeps the "fired" prefix, which
// every reader of the run history treats as a run that happened.
const ScheduleStatusFiredSilent = "fired_silent"

// --- Schedules (docs/log/38 + ADR0021) ----------------------------------------------

const scheduleCols = `SELECT id, membership_id, tenant_id, owner_conv, spec_kind, spec, spec_label, tz,
	wake_policy, session_mode, reuse_target, agent_kind, model, repo, worktree, new_branch, prompt,
	overlap_policy, enabled, next_run, last_run, last_status, created_at, updated_at,
	reuse_session, reuse_started_at, reuse_run_count, rotation, missing_target_policy,
	manual_fire_pending, report, stop_after_run, held_by_removal, deliver_to, silent FROM schedule`

func scanSchedule(row scanner) (Schedule, error) {
	var s Schedule
	var newBranch, enabled, manualFire, report, stopAfterRun, held, silent int
	err := row.Scan(&s.ID, &s.MembershipID, &s.TenantID, &s.OwnerConv, &s.SpecKind, &s.Spec, &s.SpecLabel, &s.TZ,
		&s.WakePolicy, &s.SessionMode, &s.ReuseTarget, &s.AgentKind, &s.Model, &s.Repo, &s.Worktree, &newBranch, &s.Prompt,
		&s.OverlapPolicy, &enabled, &s.NextRun, &s.LastRun, &s.LastStatus, &s.CreatedAt, &s.UpdatedAt,
		&s.ReuseSession, &s.ReuseStartedAt, &s.ReuseRunCount, &s.Rotation, &s.MissingTargetPolicy,
		&manualFire, &report, &stopAfterRun, &held, &s.DeliverTo, &silent)
	s.NewBranch = newBranch != 0
	s.Enabled = enabled != 0
	s.ManualFirePending = manualFire != 0
	s.Report = report != 0
	s.StopAfterRun = stopAfterRun != 0
	s.HeldByRemoval = held != 0
	s.Silent = silent != 0
	return s, err
}

func (s *SQL) CreateSchedule(ctx context.Context, sc Schedule) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO schedule(id, membership_id, tenant_id, owner_conv, spec_kind, spec, spec_label, tz,
		   wake_policy, session_mode, reuse_target, agent_kind, model, repo, worktree, new_branch, prompt,
		   overlap_policy, enabled, next_run, last_run, last_status, created_at, updated_at,
		   reuse_session, reuse_started_at, reuse_run_count, rotation, missing_target_policy, report,
		   stop_after_run, deliver_to, silent)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		sc.ID, sc.MembershipID, sc.TenantID, sc.OwnerConv, sc.SpecKind, sc.Spec, sc.SpecLabel, sc.TZ,
		sc.WakePolicy, sc.SessionMode, sc.ReuseTarget, sc.AgentKind, sc.Model, sc.Repo, sc.Worktree, b2i(sc.NewBranch), sc.Prompt,
		sc.OverlapPolicy, b2i(sc.Enabled), sc.NextRun, sc.LastRun, sc.LastStatus, sc.CreatedAt, sc.UpdatedAt,
		sc.ReuseSession, sc.ReuseStartedAt, sc.ReuseRunCount, sc.Rotation, sc.MissingTargetPolicy, b2i(sc.Report),
		b2i(sc.StopAfterRun), sc.DeliverTo, b2i(sc.Silent))
	return err
}

func (s *SQL) GetSchedule(ctx context.Context, id string) (Schedule, bool, error) {
	sc, err := scanSchedule(s.db.QueryRowContext(ctx, scheduleCols+` WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return Schedule{}, false, nil
	}
	return sc, err == nil, err
}

func (s *SQL) ListSchedules(ctx context.Context, membershipID string) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx, scheduleCols+` WHERE membership_id=? ORDER BY created_at`, membershipID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// ListDueSchedules returns enabled rows with a non-empty next_run at or before
// nowRFC. The empty-string guard matters: next_run=” (a paused/spent schedule)
// must never look due, and ” sorts before any real RFC3339 stamp.
func (s *SQL) ListDueSchedules(ctx context.Context, nowRFC string) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx,
		scheduleCols+` WHERE enabled=1 AND next_run!='' AND next_run<=? ORDER BY next_run`, nowRFC)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *SQL) UpdateSchedule(ctx context.Context, sc Schedule) error {
	// membership_id in the WHERE so a member can only update their own row.
	_, err := s.db.ExecContext(ctx,
		`UPDATE schedule SET owner_conv=?, spec_kind=?, spec=?, spec_label=?, tz=?, wake_policy=?,
		   session_mode=?, reuse_target=?, agent_kind=?, model=?, repo=?, worktree=?, new_branch=?, prompt=?,
		   overlap_policy=?, enabled=?, next_run=?, updated_at=?, rotation=?, missing_target_policy=?, report=?,
		   stop_after_run=?, deliver_to=?, silent=?, held_by_removal=0
		 WHERE id=? AND membership_id=?`,
		sc.OwnerConv, sc.SpecKind, sc.Spec, sc.SpecLabel, sc.TZ, sc.WakePolicy,
		sc.SessionMode, sc.ReuseTarget, sc.AgentKind, sc.Model, sc.Repo, sc.Worktree, b2i(sc.NewBranch), sc.Prompt,
		sc.OverlapPolicy, b2i(sc.Enabled), sc.NextRun, sc.UpdatedAt, sc.Rotation, sc.MissingTargetPolicy, b2i(sc.Report),
		b2i(sc.StopAfterRun), sc.DeliverTo, b2i(sc.Silent), sc.ID, sc.MembershipID)
	return err
}

// SetScheduleReuse persists the reuse ledger (P6) after a reuse fire: which long-lived
// session is now current, when it started, and the fire count since the last rotation.
// Kept separate from RecordScheduleFire (which advances the cron ledger) because the
// firer computes these before the fire ledger is stamped, and only reuse schedules touch
// them.
func (s *SQL) SetScheduleReuse(ctx context.Context, id, reuseSession, reuseStartedAt string, runCount int, updatedAt string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE schedule SET reuse_session=?, reuse_started_at=?, reuse_run_count=?, updated_at=? WHERE id=?`,
		reuseSession, reuseStartedAt, runCount, updatedAt, id)
	return err
}

func (s *SQL) SetScheduleEnabled(ctx context.Context, id, membershipID string, enabled bool, nextRun, updatedAt string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE schedule SET enabled=?, next_run=?, updated_at=?, held_by_removal=0 WHERE id=? AND membership_id=?`,
		b2i(enabled), nextRun, updatedAt, id, membershipID)
	return err
}

func (s *SQL) DeleteSchedule(ctx context.Context, id, membershipID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM schedule WHERE id=? AND membership_id=?`, id, membershipID)
	return err
}

func (s *SQL) RecordScheduleFire(ctx context.Context, id, lastRun, lastStatus, nextRun string, enabled bool, updatedAt string) error {
	// Clear manual_fire_pending on every fire: the run-now signal is consumed once the
	// fire it requested has happened (the scheduler already read it to tag the run).
	_, err := s.db.ExecContext(ctx,
		`UPDATE schedule SET last_run=?, last_status=?, next_run=?, enabled=?, updated_at=?, manual_fire_pending=0, held_by_removal=0 WHERE id=?`,
		lastRun, lastStatus, nextRun, b2i(enabled), updatedAt, id)
	return err
}

// HoldScheduleForRemoval records a slot that came due while the owner's membership was
// inactive: the fire is stamped as skipped and the row paused and marked held_by_removal,
// in one statement that applies only while the membership is still not active AND the row
// is still the enabled one the scheduler listed at slot (enabled=1, next_run=slot). held=
// false means nothing was written: the person was re-invited first, or the owner paused,
// edited or re-ran the row after it was listed — a hold must never be stamped on top of
// the owner's own change, or the next re-invite would resume what they stopped.
func (s *SQL) HoldScheduleForRemoval(ctx context.Context, id, slot, lastRun, lastStatus, updatedAt string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE schedule SET last_run=?, last_status=?, next_run='', enabled=0, updated_at=?,
		   manual_fire_pending=0, held_by_removal=1
		 WHERE id=? AND enabled=1 AND next_run=?
		   AND NOT EXISTS (SELECT 1 FROM membership m WHERE m.id=schedule.membership_id AND m.status='active')`,
		lastRun, lastStatus, updatedAt, id, slot)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ResumeScheduleHeldByRemoval re-enables a row only while it still carries
// held_by_removal, so an owner's pause landing between the caller's read and this write
// wins. resumed=false means there was nothing to resume.
func (s *SQL) ResumeScheduleHeldByRemoval(ctx context.Context, id, membershipID, nextRun, updatedAt string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE schedule SET enabled=1, next_run=?, updated_at=?, held_by_removal=0
		 WHERE id=? AND membership_id=? AND held_by_removal=1`,
		nextRun, updatedAt, id, membershipID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// MarkManualFirePending flags a run-now request: it sets next_run so the ticker fires the
// schedule immediately AND records that this next fire was manually triggered, so the run
// history can distinguish it from an automatic scheduled fire (docs/log/38). enabled is forced
// true (run-now on a paused schedule is rejected earlier). membership_id scopes the write.
func (s *SQL) MarkManualFirePending(ctx context.Context, id, membershipID, nextRun, updatedAt string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE schedule SET enabled=1, next_run=?, manual_fire_pending=1, updated_at=?, held_by_removal=0 WHERE id=? AND membership_id=?`,
		nextRun, updatedAt, id, membershipID)
	return err
}

func (s *SQL) AppendScheduleRun(ctx context.Context, run ScheduleRun, keepN int) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO schedule_run(id, schedule_id, membership_id, fired_at, status, detail, session, trigger_kind, slot) VALUES(?,?,?,?,?,?,?,?,?)`,
		run.ID, run.ScheduleID, run.MembershipID, run.FiredAt, run.Status, run.Detail, run.Session, run.Trigger, run.Slot); err != nil {
		return err
	}
	if keepN <= 0 {
		return nil
	}
	// Trim to the keepN most recent rows for this schedule. The NOT IN (…LIMIT…)
	// subquery is dialect-neutral across SQLite and Postgres.
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM schedule_run WHERE schedule_id=? AND id NOT IN (
		   SELECT id FROM schedule_run WHERE schedule_id=? ORDER BY fired_at DESC LIMIT ?)`,
		run.ScheduleID, run.ScheduleID, keepN)
	return err
}

func (s *SQL) MarkScheduleRunNotExecuted(ctx context.Context, scheduleID, membershipID, session, slot, status, detail string) (bool, bool, error) {
	if slot == "" {
		return false, false, nil
	}
	// The status condition sits in the UPDATE, not in choosing the run: a repeated report
	// must find the run it already marked and change nothing, never move on to a later run.
	res, err := s.db.ExecContext(ctx,
		`UPDATE schedule_run SET status=?, detail=?
		 WHERE schedule_id=? AND membership_id=? AND session=? AND slot=? AND status LIKE 'fired%'`,
		status, detail, scheduleID, membershipID, session, slot)
	if err != nil {
		return false, false, err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return n > 0, n > 0, err
	}
	var n int
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schedule_run WHERE schedule_id=? AND membership_id=? AND session=? AND slot=?`,
		scheduleID, membershipID, session, slot).Scan(&n)
	return n > 0, false, err
}

func (s *SQL) MarkScheduleRunSilent(ctx context.Context, scheduleID, membershipID, session, slot string) (bool, bool, error) {
	if slot == "" {
		return false, false, nil
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE schedule_run SET status=?
		 WHERE schedule_id=? AND membership_id=? AND session=? AND slot=? AND status='fired'`,
		ScheduleStatusFiredSilent, scheduleID, membershipID, session, slot)
	if err != nil {
		return false, false, err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return n > 0, n > 0, err
	}
	var n int
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schedule_run WHERE schedule_id=? AND membership_id=? AND session=? AND slot=?`,
		scheduleID, membershipID, session, slot).Scan(&n)
	return n > 0, false, err
}

func (s *SQL) ListScheduleRuns(ctx context.Context, scheduleID, membershipID string, limit int) ([]ScheduleRun, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, schedule_id, membership_id, fired_at, status, detail, session, trigger_kind, slot FROM schedule_run
		 WHERE schedule_id=? AND membership_id=? ORDER BY fired_at DESC LIMIT ?`,
		scheduleID, membershipID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduleRun
	for rows.Next() {
		var r ScheduleRun
		if err := rows.Scan(&r.ID, &r.ScheduleID, &r.MembershipID, &r.FiredAt, &r.Status, &r.Detail, &r.Session, &r.Trigger, &r.Slot); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
