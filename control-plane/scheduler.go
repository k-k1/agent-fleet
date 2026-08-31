package main

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
	// Embed the IANA tz database so cron evaluation resolves zones (and DST) even on
	// a minimal image with no /usr/share/zoneinfo. Without this, scheduleLocation
	// would silently degrade every non-UTC schedule to UTC (docs/log/38 TZ/DST). The
	// embedded copy is only consulted when the OS database is absent, so it is a
	// safe ~450KB fallback that never overrides a present system zoneinfo.
	_ "time/tzdata"
)

// Scheduled execution (docs/log/38 + ADR0021). A single CP-resident goroutine — the
// counterpart to the reaper (reaper.go) — watches the wall clock and drives due
// schedules. It exists on the CP, not in the workspace, because the CP is the only
// thing alive while a workspace is stopped, and only the CP can wake it.
//
// This file is P1: the tick loop, the fire ledger (next_run bookkeeping), and the
// cron/interval/once evaluator. The actual "wake the workspace and inject a session"
// is P2 and lives behind the scheduleFirer seam, so the skeleton is safe to run on
// its own — the default firer only logs and never touches a workspace.
//
// TZ/DST semantics (self-contained evaluator, no external cron dep): cron fields are
// matched against wall-clock time in the schedule's IANA zone while stepping through
// absolute time minute by minute. A spring-forward gap (a wall minute that does not
// exist) is naturally skipped. A fall-back repeat (a wall minute that occurs twice)
// fires only once: nextCron refuses a candidate whose wall Y/M/D/H/M equals the
// reference instant's, so the duplicated hour does not double-fire.

// scheduleFirer performs the side-effecting execution of a due schedule. P1 ships
// logFirer (no-op + log); P2 implements wake + create_session injection against this
// same interface. slot is the fire instant taken from next_run. The returned status
// is stamped into last_status (short token, e.g. "fired" / "skipped_rate_limited").
// session is the session the fire drove (created for session_mode=new, the reuse target
// for session_mode=reuse) so the run history can link to it; empty when nothing ran.
type scheduleFirer interface {
	fire(ctx context.Context, sch Schedule, slot time.Time) (status, session string, err error)
}

// logFirer is the P1 default: it records that a schedule came due without waking any
// workspace, so the ledger advances and the loop can be exercised end-to-end before
// the P2 wake path exists.
type logFirer struct{}

func (logFirer) fire(_ context.Context, sch Schedule, slot time.Time) (string, string, error) {
	log.Printf("scheduler: schedule %s due at %s (P1 no-op firer — wake/inject is P2; kind=%s repo=%s)",
		sch.ID, slot.UTC().Format(time.RFC3339), sch.AgentKind, sch.Repo)
	return "fired_noop", "", nil
}

// scheduleStore is the narrow store view the scheduler needs (docs/log/23 narrow view).
type scheduleStore interface {
	ListDueSchedules(ctx context.Context, nowRFC string) ([]Schedule, error)
	RecordScheduleFire(ctx context.Context, id, lastRun, lastStatus, nextRun string, enabled bool, updatedAt string) error
	AppendScheduleRun(ctx context.Context, run ScheduleRun, keepN int) error
	// InsertNotification surfaces an unattended failure/skip in the notification center
	// (★3, P4) — WS-independent because the CP notification store is the durable sink.
	InsertNotification(ctx context.Context, n Notification) error
}

// scheduleRunKeep bounds the per-schedule run history (docs/log/38 P3 get_schedule_runs).
const scheduleRunKeep = 50

// statusMembershipInactive is the outcome the firer reports when the schedule's owner
// membership is no longer active (revoked / tenant access removed). It is durable, not a
// blip — the lookup succeeded and found no active membership — so fireOne disables the
// row on it rather than letting it no-op forever where nobody can see it.
const statusMembershipInactive = "skipped_membership_inactive"

// scheduleJitterMax caps the deterministic per-schedule fire jitter (★2 thundering-herd
// → host-OOM mitigation). Set once at startup from AF_SCHEDULE_JITTER; 0 disables. A
// package global (not a param) so both initialNextRun and advanceNextRun apply it
// without threading config through the operator API. Defaults to 0 so unit tests that
// assert exact fire instants are unaffected.
var scheduleJitterMax time.Duration

// schedulerRunning reports whether the CP scheduler goroutine was started this process
// (AF_SCHEDULER_INTERVAL > 0). The operator API reads it so create_schedule / run_now can
// warn that a definition will never fire on a deployment where the scheduler is disabled
// (the default) — otherwise those calls succeed silently and nothing ever runs. Set once
// at startup in main.go before the HTTP server serves; read-only afterward.
var schedulerRunning bool

// scheduleJitter returns a stable offset in [0, scheduleJitterMax] derived from the
// schedule id, so many schedules aligned to the same wall-clock (e.g. everyone at 09:00)
// fire spread across a window instead of all waking at once. Deterministic — the same
// schedule always jitters by the same amount — so the deferral is reproducible across CP
// restarts.
//
// Jitter is applied as a FIRE-TIME GATE (see tick), not baked into next_run: next_run
// stays the nominal wall-clock instant, so the operator's read-back confirmation, the
// next_run_local display, and the {{time}} prompt variable all show the time the user
// actually asked for (09:00, not 09:01), and the idempotency slot stays the nominal
// instant. Keeping next_run nominal also makes the gate immune to an AF_SCHEDULE_JITTER
// change across a restart. jitterForSchedule limits it to cron (an interval is already
// spread by its own phase, and deferring it each period would drift it; a once is an
// exact user-chosen instant we must not move).
func scheduleJitter(scheduleID string) time.Duration {
	if scheduleJitterMax <= 0 || scheduleID == "" {
		return 0
	}
	maxSecs := int64(scheduleJitterMax / time.Second)
	if maxSecs <= 0 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(scheduleID))
	return time.Duration(h.Sum64()%uint64(maxSecs+1)) * time.Second
}

// jitterForSchedule is the fire deferral applied to a due schedule: the deterministic
// per-id jitter for cron, zero for interval/once (which must fire on their exact slot).
func jitterForSchedule(sch Schedule) time.Duration {
	if sch.SpecKind != "cron" {
		return 0
	}
	return scheduleJitter(sch.ID)
}

type scheduler struct {
	store    scheduleStore
	firer    scheduleFirer
	interval time.Duration
}

func newScheduler(store scheduleStore, firer scheduleFirer, interval time.Duration) *scheduler {
	if firer == nil {
		firer = logFirer{}
	}
	return &scheduler{store: store, firer: firer, interval: interval}
}

func (sc *scheduler) run(ctx context.Context) {
	log.Printf("scheduler: interval=%s (P1 skeleton — firer=%T)", sc.interval, sc.firer)
	t := time.NewTicker(sc.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sc.tick(ctx)
		}
	}
}

// tick fires every schedule that is due as of now, advancing each one's ledger.
func (sc *scheduler) tick(ctx context.Context) { sc.tickAt(ctx, time.Now().UTC()) }

// schedulerMaxConcurrentFires bounds how many due schedules fire in parallel per
// tick: enough that one cold wake (up to ~5 min) or assistant turn (up to ~8 min)
// no longer delays every other schedule until it finishes, small enough not to
// stampede the host with simultaneous container wakes.
const schedulerMaxConcurrentFires = 4

// tickAt is tick with an injected clock so the jitter gate is unit-testable.
// Due schedules fire CONCURRENTLY (capped); the tick still waits for them all, so
// a schedule can never double-fire — the next tick only lists rows whose ledger
// this one has already advanced.
func (sc *scheduler) tickAt(ctx context.Context, now time.Time) {
	due, err := sc.store.ListDueSchedules(ctx, now.Format(time.RFC3339))
	if err != nil {
		log.Printf("scheduler: list due: %v", err)
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, schedulerMaxConcurrentFires)
	for _, sch := range due {
		// Jitter gate (★2): next_run is the nominal slot, but a cron fire is held back
		// by its deterministic per-id offset so aligned schedules (everyone at 09:00) do
		// not wake at once. A row stays "due" in the SQL sense but is skipped this tick
		// until now reaches slot+jitter; it fires on a later tick. interval/once have no
		// jitter so they fire immediately. A row with an unparseable next_run falls
		// through to fireOne (which defends with slot=now).
		if slot, err := time.Parse(time.RFC3339, sch.NextRun); err == nil {
			if now.Before(slot.Add(jitterForSchedule(sch))) {
				continue
			}
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(sch Schedule) {
			defer func() { <-sem; wg.Done() }()
			sc.fireOne(ctx, sch, now)
		}(sch)
	}
	wg.Wait()
}

// fireOne runs the firer for one due schedule then advances its ledger: last_run/
// last_status stamped, next_run recomputed (cron/interval) or cleared and the row
// disabled (a spent "once"). A firer error is recorded as an "error:" status but
// does not stop the ledger from advancing — otherwise a permanently failing schedule
// would re-fire every tick.
func (sc *scheduler) fireOne(ctx context.Context, sch Schedule, now time.Time) {
	slot, err := time.Parse(time.RFC3339, sch.NextRun)
	if err != nil {
		slot = now // defensive: a corrupt next_run should not wedge the loop
	}
	status, session, ferr := sc.firer.fire(ctx, sch, slot)
	if ferr != nil {
		status = "error:" + truncStatus(ferr.Error())
	}
	// Compute the next fire strictly after `now` (not after the slot) so a backlog
	// from a stopped CP collapses to a single catch-up fire rather than replaying
	// every missed slot on the next tick.
	next, keep, cerr := advanceNextRun(sch, now)
	if cerr != nil {
		// Cannot compute the next slot (bad spec/tz) — disable so it stops re-firing
		// and surface why in the status. P4 turns this into an operator report.
		log.Printf("scheduler: schedule %s advance: %v (disabling)", sch.ID, cerr)
		next, keep = "", false
		if status == "fired_noop" || strings.HasPrefix(status, "fired") {
			status = "error:" + truncStatus(cerr.Error())
		}
	}
	// The owner membership is gone (suspended, or the row deleted — `schedule` has no FK
	// to membership, so its rows outlive it). Everything about this schedule is reachable
	// ONLY through that membership: the Console lists by membership, its run history is
	// scoped the same way, and the unattended-failure notification below either lands in a
	// mailbox nobody can open or fails outright on notification's membership FK. Left
	// enabled it would no-op on every slot forever, appending run rows no one will ever
	// read — an invisible schedule that quietly keeps ticking. Disable it instead: the
	// membership id is stable per (identity, tenant) (EnsureMembership upserts on that
	// pair), so restoring access brings the row back as a PAUSED schedule whose owner can
	// read the reason in the ledger and resume it.
	if status == statusMembershipInactive {
		log.Printf("scheduler: schedule %s owner membership %s is inactive — disabling "+
			"(nothing can run and nobody can see it; resume it after restoring access)", sch.ID, sch.MembershipID)
		next, keep = "", false
	}
	enabled := sch.Enabled && keep
	nowRFC := now.UTC().Format(time.RFC3339)
	if err := sc.store.RecordScheduleFire(ctx, sch.ID, nowRFC, status, next, enabled, nowRFC); err != nil {
		// 台帳が前進しないと次 tick で同じ slot を再発火する。reuse/assistant 経路には
		// slot 単位の冪等機構が無く、プロンプトの二重配達になり得るので目立たせる。
		log.Printf("scheduler: WARNING record fire %s failed — ledger did not advance; "+
			"next tick may re-fire this slot (possible duplicate prompt delivery): %v", sch.ID, err)
	}
	// Append to the run history (docs/log/38 P3). Best-effort: a failed history write must
	// not affect the ledger advance above. Session links the run to what it drove; trigger
	// records whether this was a manual run-now (flag set on the row) or an automatic fire.
	trigger := "scheduled"
	if sch.ManualFirePending {
		trigger = "manual"
	}
	run := ScheduleRun{ID: newID(), ScheduleID: sch.ID, MembershipID: sch.MembershipID, FiredAt: nowRFC, Status: status, Session: session, Trigger: trigger}
	if err := sc.store.AppendScheduleRun(ctx, run, scheduleRunKeep); err != nil {
		log.Printf("scheduler: append run %s: %v", sch.ID, err)
	}
	// Unattended failure/skip must not be silent (★3, P4): surface it in the
	// notification center, which is the WS-independent durable sink (the CP store,
	// unlike the operator conversation which lives in the possibly-stopped agent).
	// A successful fire reports itself via the session's report_to (docs/log/30).
	if scheduleNotifyStatus(status) {
		sc.notifyOutcome(ctx, sch, slot, status, nowRFC)
	}
}

// scheduleNotifyStatus reports whether an outcome deserves an unattended-failure
// notification. Hard failures always do; among the soft skips, quota/rate-limit/
// membership/target-missing are surprises worth surfacing. skipped_overlap is also
// surfaced: the fire WAS due and expected to deliver but couldn't because the reuse
// target's prior turn is still running — a busy-forever target would otherwise silently
// never run, the same "unattended non-delivery" class as the sbk7oej drop. Only
// skipped_stopped stays quiet — a policy "skip" of a stopped workspace is the user's
// explicit no-wake choice where nothing was expected to run (recorded in history only).
func scheduleNotifyStatus(status string) bool {
	if strings.HasPrefix(status, "error") {
		return true
	}
	switch status {
	case "skipped_quota", "skipped_rate_limited", statusMembershipInactive,
		"skipped_target_missing", "skipped_overlap":
		return true
	}
	return false
}

// notifyOutcome inserts a membership-scoped notification for a failed/skipped fire. The
// EventID is deterministic per (schedule, slot) so a CP-restart re-fire of the same slot
// does not double-notify (InsertNotification is ON CONFLICT DO NOTHING).
func (sc *scheduler) notifyOutcome(ctx context.Context, sch Schedule, slot time.Time, status, nowRFC string) {
	label := sch.SpecLabel
	if label == "" {
		label = sch.ID
	}
	payload, _ := json.Marshal(map[string]any{
		"schedule_id": sch.ID, "status": status, "spec_label": sch.SpecLabel, "spec": sch.Spec,
	})
	kind := "schedule-failed"
	if strings.HasPrefix(status, "skipped") {
		kind = "schedule-skipped"
	}
	n := Notification{
		EventID:      "sched-" + status + "-" + sch.ID + "-" + slot.UTC().Format(time.RFC3339),
		MembershipID: sch.MembershipID, Kind: kind,
		TargetType: "schedule", TargetID: sch.ID, TargetKind: sch.AgentKind,
		DisplayName: label, Payload: string(payload), CreatedAt: nowRFC,
	}
	if err := sc.store.InsertNotification(ctx, n); err != nil {
		log.Printf("scheduler: notify outcome %s: %v", sch.ID, err)
	}
}

func truncStatus(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// scheduleLocation resolves a schedule's IANA zone, defaulting to UTC when the tz is
// blank or unknown (a bad tz should degrade to UTC, not wedge evaluation).
func scheduleLocation(tz string) *time.Location {
	if tz == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

// initialNextRun computes the first fire time for a freshly created (or re-armed)
// schedule, as a UTC RFC3339 string. `from` is the reference instant (typically now).
//   - once:     the absolute Spec instant (may be in the past — the wake policy then
//     decides catch-up vs skip; P1 fires once immediately then disables).
//   - cron:     the next wall-clock match at/after `from`.
//   - interval: from + interval.
func initialNextRun(sch Schedule, from time.Time) (string, error) {
	switch sch.SpecKind {
	case "once":
		t, err := parseOnce(sch.Spec)
		if err != nil {
			return "", err
		}
		return t.UTC().Format(time.RFC3339), nil
	case "cron":
		t, err := nextCron(sch.Spec, from.Add(-time.Second), scheduleLocation(sch.TZ))
		if err != nil {
			return "", err
		}
		// Nominal instant (no jitter) — jitter is a fire-time gate (see tick), so next_run
		// reads back to the operator as the exact time the user asked for.
		return t.UTC().Format(time.RFC3339), nil
	case "interval":
		d, err := intervalDuration(sch.Spec)
		if err != nil {
			return "", err
		}
		return from.Add(d).UTC().Format(time.RFC3339), nil
	default:
		return "", fmt.Errorf("unknown spec_kind %q", sch.SpecKind)
	}
}

// advanceNextRun computes the next fire after a schedule has just fired. `after` is
// the fire instant. keepEnabled is false only for a spent "once" (nothing more to
// run). Returns ("", false, nil) in that case.
func advanceNextRun(sch Schedule, after time.Time) (nextRun string, keepEnabled bool, err error) {
	switch sch.SpecKind {
	case "once":
		return "", false, nil
	case "cron":
		t, err := nextCron(sch.Spec, after, scheduleLocation(sch.TZ))
		if err != nil {
			return "", false, err
		}
		// Nominal instant (no jitter) — jitter is applied as a fire-time gate (see tick).
		return t.UTC().Format(time.RFC3339), true, nil
	case "interval":
		d, err := intervalDuration(sch.Spec)
		if err != nil {
			return "", false, err
		}
		return after.Add(d).UTC().Format(time.RFC3339), true, nil
	default:
		return "", false, fmt.Errorf("unknown spec_kind %q", sch.SpecKind)
	}
}

// validateSpec checks a schedule spec+tz without computing a fire time — used by the
// operator API (P3) to reject a bad cron/interval/once or tz at create/update.
func validateSpec(kind, spec, tz string) error {
	switch kind {
	case "cron":
		if _, err := parseCron(spec); err != nil {
			return err
		}
	case "interval":
		if _, err := intervalDuration(spec); err != nil {
			return err
		}
	case "once":
		if _, err := parseOnce(spec); err != nil {
			return err
		}
	default:
		return fmt.Errorf("spec_kind must be cron, interval, or once")
	}
	if tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			return fmt.Errorf("unknown tz %q", tz)
		}
	}
	return nil
}

func parseOnce(spec string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(spec))
	if err != nil {
		return time.Time{}, fmt.Errorf("once spec %q: want RFC3339: %w", spec, err)
	}
	return t, nil
}

// minIntervalSecs is the frequency floor (docs/log/38 頻度下限): sub-minute schedules are
// rejected so a runaway interval cannot hammer the fleet. The scheduler ticks per
// minute anyway, so anything below this is meaningless.
const minIntervalSecs = 60

func intervalDuration(spec string) (time.Duration, error) {
	secs, err := strconv.Atoi(strings.TrimSpace(spec))
	if err != nil {
		return 0, fmt.Errorf("interval spec %q: want whole seconds: %w", spec, err)
	}
	if secs < minIntervalSecs {
		return 0, fmt.Errorf("interval %ds below floor of %ds", secs, minIntervalSecs)
	}
	return time.Duration(secs) * time.Second, nil
}

// --- cron evaluator -------------------------------------------------------------

// cronFields is a parsed 5-field cron expression (minute hour dom month dow). Each
// set holds the permitted values; domStar/dowStar record whether that field was "*"
// so day matching can apply the classic Vixie rule (see cronMatch).
type cronFields struct {
	minute, hour, dom, month, dow map[int]bool
	domStar, dowStar              bool
}

// parseCron parses a standard 5-field cron expression. Supports "*", steps ("*/n",
// "a-b/n", "a/n"), ranges ("a-b"), lists ("a,b,c") and any combination. dow accepts
// 0-7 with both 0 and 7 meaning Sunday.
func parseCron(spec string) (cronFields, error) {
	parts := strings.Fields(strings.TrimSpace(spec))
	if len(parts) != 5 {
		return cronFields{}, fmt.Errorf("cron %q: want 5 fields, got %d", spec, len(parts))
	}
	var f cronFields
	var err error
	if f.minute, _, err = parseCronField(parts[0], 0, 59); err != nil {
		return f, fmt.Errorf("cron minute: %w", err)
	}
	if f.hour, _, err = parseCronField(parts[1], 0, 23); err != nil {
		return f, fmt.Errorf("cron hour: %w", err)
	}
	if f.dom, f.domStar, err = parseCronField(parts[2], 1, 31); err != nil {
		return f, fmt.Errorf("cron day-of-month: %w", err)
	}
	if f.month, _, err = parseCronField(parts[3], 1, 12); err != nil {
		return f, fmt.Errorf("cron month: %w", err)
	}
	if f.dow, f.dowStar, err = parseCronField(parts[4], 0, 7); err != nil {
		return f, fmt.Errorf("cron day-of-week: %w", err)
	}
	// Normalize dow 7 -> 0 (both are Sunday) so matching against time.Weekday works.
	if f.dow[7] {
		f.dow[0] = true
		delete(f.dow, 7)
	}
	return f, nil
}

// parseCronField parses one field into the set of permitted ints in [min,max].
// isStar reports whether the field was a bare "*" (needed for the day-match rule).
func parseCronField(field string, min, max int) (map[int]bool, bool, error) {
	set := map[int]bool{}
	isStar := field == "*"
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return nil, false, fmt.Errorf("empty term in %q", field)
		}
		rng := part
		step := 1
		if slash := strings.IndexByte(part, '/'); slash >= 0 {
			var err error
			if step, err = strconv.Atoi(part[slash+1:]); err != nil || step <= 0 {
				return nil, false, fmt.Errorf("bad step in %q", part)
			}
			rng = part[:slash]
		}
		lo, hi := min, max
		switch {
		case rng == "*":
			// full range with the step applied below
		case strings.IndexByte(rng, '-') >= 0:
			dash := strings.IndexByte(rng, '-')
			a, err1 := strconv.Atoi(rng[:dash])
			b, err2 := strconv.Atoi(rng[dash+1:])
			if err1 != nil || err2 != nil {
				return nil, false, fmt.Errorf("bad range %q", rng)
			}
			lo, hi = a, b
		default:
			v, err := strconv.Atoi(rng)
			if err != nil {
				return nil, false, fmt.Errorf("bad value %q", rng)
			}
			// "a/n" (single with a step) counts from a up to max; a bare "a" is just a.
			lo = v
			if strings.IndexByte(part, '/') >= 0 {
				hi = max
			} else {
				hi = v
			}
		}
		if lo < min || hi > max || lo > hi {
			return nil, false, fmt.Errorf("term %q out of range [%d,%d]", part, min, max)
		}
		for v := lo; v <= hi; v += step {
			set[v] = true
		}
	}
	if len(set) == 0 {
		return nil, false, fmt.Errorf("no values in %q", field)
	}
	return set, isStar, nil
}

// cronMatch reports whether wall-clock time t satisfies the cron fields. Day-of-month
// and day-of-week combine with the classic Vixie rule: when both are restricted a day
// matches if EITHER matches; when only one is restricted only that one applies.
func cronMatch(f cronFields, t time.Time) bool {
	if !f.minute[t.Minute()] || !f.hour[t.Hour()] || !f.month[int(t.Month())] {
		return false
	}
	domOK := f.dom[t.Day()]
	dowOK := f.dow[int(t.Weekday())]
	switch {
	case f.domStar && f.dowStar:
		return true
	case !f.domStar && !f.dowStar:
		return domOK || dowOK
	case !f.domStar:
		return domOK
	default:
		return dowOK
	}
}

// nextCron returns the first instant strictly after `after` whose wall-clock time in
// loc matches the cron expression. It steps minute by minute over a bounded horizon
// (a valid cron always fires within ~a year). See the DST note at the top of the file
// for spring-forward (skipped) and fall-back (fire-once) handling.
func nextCron(spec string, after time.Time, loc *time.Location) (time.Time, error) {
	f, err := parseCron(spec)
	if err != nil {
		return time.Time{}, err
	}
	prev := after.In(loc)
	// Start at the next whole minute strictly after `after`.
	start := after.Truncate(time.Minute).Add(time.Minute)
	// Four leap years of minutes plus slack. A year-wide horizon rejected a legitimate
	// Feb-29-only cron ("0 0 29 2 *") whose next match can be up to ~4 years out; four
	// years covers the leap cycle. (The 2100 non-leap century boundary is not covered —
	// an accepted, negligible edge.)
	const horizon = 4*366*24*60 + 60
	for i := 0; i < horizon; i++ {
		t := start.Add(time.Duration(i) * time.Minute)
		wt := t.In(loc)
		if !cronMatch(f, wt) {
			continue
		}
		// Fall-back guard: the same wall minute can recur an hour later in absolute
		// time; do not fire twice for the wall minute equal to the reference.
		if sameWallMinute(wt, prev) {
			continue
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("cron %q: no fire time within a year", spec)
}

func sameWallMinute(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd && a.Hour() == b.Hour() && a.Minute() == b.Minute()
}
