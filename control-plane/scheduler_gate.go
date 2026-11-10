package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Gated schedules (ADR 0021 addendum of 2026-11-10, issue #1763 slice 1). A schedule may carry
// a gate: on each tick the scheduler runs a cheap check first and fires the ordinary new-session
// path only when the check says so AND the result differs from the one it last fired on.
//
// The one gate kind is declarative: one of the member's saved work-item queries (ADR 0061),
// resolved by the running Agent through POST /work-items/fetch. No code runs, no token reaches
// the Control Plane, and nothing is evaluated while the workspace is stopped (a probe wake is a
// later slice). The check fails closed: an error never fires.

const (
	gateKindWorkItems = "work_items"

	gateFingerprintKeys        = "keys"
	gateFingerprintKeysUpdated = "keys+updated"

	// Defaults and bounds of the per-schedule caps. A missed triage leaves the red red and the
	// next change catches it, while a duplicate burns quota, so the defaults lean quiet.
	gateDefaultMaxFiresPerDay = 2
	gateMaxFiresPerDayLimit   = 24
	gateDefaultMinFireGapS    = 6 * 3600
	gateMinFireGapSFloor      = 60
	gateMinFireGapSLimit      = 24 * 3600

	// gateRefsMax bounds {{gate.refs}}.
	gateRefsMax = 20
)

// Verdicts stored in schedule.gate_last_verdict. Only gateVerdictFire starts a session. An
// error is stored as "error:" and the message.
const (
	gateVerdictClear      = "clear"
	gateVerdictUnchanged  = "unchanged"
	gateVerdictCappedGap  = "capped_gap"
	gateVerdictCappedDay  = "capped_day"
	gateVerdictFire       = "fire"
	gateVerdictNotRunning = "not_running"
	gateVerdictUnreached  = "unreachable"
	gateVerdictErrorPfx   = "error:"
)

// gateWorkItemsSpec is gate_spec for gate_kind=work_items.
type gateWorkItemsSpec struct {
	QueryID        string `json:"query_id"`
	Fingerprint    string `json:"fingerprint,omitempty"`
	MaxFiresPerDay int    `json:"max_fires_per_day,omitempty"`
	MinFireGapS    int    `json:"min_fire_gap_s,omitempty"`
}

// withDefaults fills the omitted fields.
func (g gateWorkItemsSpec) withDefaults() gateWorkItemsSpec {
	if g.Fingerprint == "" {
		g.Fingerprint = gateFingerprintKeys
	}
	if g.MaxFiresPerDay == 0 {
		g.MaxFiresPerDay = gateDefaultMaxFiresPerDay
	}
	if g.MinFireGapS == 0 {
		g.MinFireGapS = gateDefaultMinFireGapS
	}
	return g
}

// parseGateSpec reads a stored gate_spec. A row that cannot be read is a gate error at fire time,
// never a fire.
func parseGateSpec(raw string) (gateWorkItemsSpec, error) {
	var g gateWorkItemsSpec
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		return g, fmt.Errorf("gate spec is not valid JSON: %w", err)
	}
	g = g.withDefaults()
	if err := g.validate(); err != nil {
		return g, err
	}
	return g, nil
}

func (g gateWorkItemsSpec) validate() error {
	if strings.TrimSpace(g.QueryID) == "" {
		return errors.New("query_id is required")
	}
	if !oneOf(g.Fingerprint, gateFingerprintKeys, gateFingerprintKeysUpdated) {
		return errors.New("fingerprint must be keys or keys+updated")
	}
	if g.MaxFiresPerDay < 1 || g.MaxFiresPerDay > gateMaxFiresPerDayLimit {
		return fmt.Errorf("max_fires_per_day must be 1..%d", gateMaxFiresPerDayLimit)
	}
	if g.MinFireGapS < gateMinFireGapSFloor || g.MinFireGapS > gateMinFireGapSLimit {
		return fmt.Errorf("min_fire_gap_s must be %d..%d", gateMinFireGapSFloor, gateMinFireGapSLimit)
	}
	return nil
}

// gateItem is the part of a matched work item the gate reads: its key and, for
// fingerprint=keys+updated, its updated_at.
type gateItem struct{ Key, UpdatedAt string }

// gateState is the gate ledger of a schedule row.
type gateState struct {
	FiredFingerprint string
	FiredAt          time.Time // zero when the gate never fired
	WindowStart      time.Time // zero when no window is open
	WindowCount      int
}

func gateStateOf(sch store.Schedule) gateState {
	st := gateState{FiredFingerprint: sch.GateFiredFingerprint, WindowCount: sch.GateWindowCount}
	st.FiredAt, _ = time.Parse(time.RFC3339, sch.GateFiredAt)
	st.WindowStart, _ = time.Parse(time.RFC3339, sch.GateWindowStart)
	return st
}

// gateDecision is the pure outcome of one evaluation. WindowStart / WindowCount are the fire
// window as it stands after a fire.
type gateDecision struct {
	Verdict     string
	Fingerprint string
	Refs        []string
	WindowStart time.Time
	WindowCount int
}

// gateVerdict decides from what the check matched, what the gate last fired on and the caps.
// It is deliberately free of I/O. The order matters: no matches is clear, the same fingerprint as
// the last fire is unchanged (so a flapping red is not reported as capped), and only a changed
// result is then held back by the caps.
func gateVerdict(items []gateItem, spec gateWorkItemsSpec, st gateState, now time.Time) gateDecision {
	if len(items) == 0 {
		return gateDecision{Verdict: gateVerdictClear}
	}
	fp := gateFingerprint(items, spec.Fingerprint)
	d := gateDecision{Fingerprint: fp, Refs: gateRefs(items)}
	if fp == st.FiredFingerprint {
		d.Verdict = gateVerdictUnchanged
		return d
	}
	windowStart, count := st.WindowStart, st.WindowCount
	if windowStart.IsZero() || now.Sub(windowStart) >= 24*time.Hour {
		windowStart, count = now, 0
	}
	if !st.FiredAt.IsZero() && now.Sub(st.FiredAt) < time.Duration(spec.MinFireGapS)*time.Second {
		d.Verdict = gateVerdictCappedGap
		return d
	}
	if count >= spec.MaxFiresPerDay {
		d.Verdict = gateVerdictCappedDay
		return d
	}
	d.Verdict = gateVerdictFire
	d.WindowStart, d.WindowCount = windowStart, count+1
	return d
}

// gateFingerprint hashes the matched set: the sorted keys, and with keys+updated each key's
// updated_at too. Order and duplicates in the Agent's answer do not move it.
func gateFingerprint(items []gateItem, mode string) string {
	lines := make([]string, 0, len(items))
	for _, it := range items {
		l := it.Key
		if mode == gateFingerprintKeysUpdated {
			l += "\t" + it.UpdatedAt
		}
		lines = append(lines, l)
	}
	sort.Strings(lines)
	h := sha256.New()
	prev := ""
	for i, l := range lines {
		if i > 0 && l == prev {
			continue
		}
		prev = l
		h.Write([]byte(l))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// gateRefKey is the shape a key must have to reach the prompt: owner/repo#N or PROJ-123. Titles
// and bodies are third-party text and never go in (ADR 0021 decision 10); a key that is not of
// this shape is dropped rather than quoted.
var gateRefKey = regexp.MustCompile(`^(?:[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9._-]+#[0-9]+|[A-Z][A-Z0-9_]*-[0-9]+)$`)

// gateRefs is the sorted, de-duplicated, validated list of keys behind {{gate.refs}}, at most
// gateRefsMax.
func gateRefs(items []gateItem) []string {
	seen := map[string]bool{}
	var out []string
	for _, it := range items {
		if !gateRefKey.MatchString(it.Key) || seen[it.Key] {
			continue
		}
		seen[it.Key] = true
		out = append(out, it.Key)
	}
	sort.Strings(out)
	if len(out) > gateRefsMax {
		out = out[:gateRefsMax]
	}
	return out
}

// Sentinel outcomes of a gate run that are not verdicts.
var (
	// errGateNotRunning: the workspace is not running. Slice 1 never wakes one to check.
	errGateNotRunning = errors.New("workspace is not running")
	// errGateUnreachable: the Agent did not answer (restarting, just stopped). Retried next
	// tick without a notification.
	errGateUnreachable = errors.New("agent did not answer")
	// errGateBypass: the owner has no active membership, which is not a gate matter. The fire
	// goes on so the firer records it the way it always has (the schedule is held for removal).
	errGateBypass = errors.New("gate bypassed")
)

// schedGateRunner runs the check. wakeFirer is the real one.
type schedGateRunner interface {
	runGate(ctx context.Context, sch store.Schedule) ([]gateItem, error)
}

// schedGateStore is the write half of the gate ledger.
type schedGateStore interface {
	ClaimScheduleGateFire(ctx context.Context, c store.ScheduleGateClaim) (bool, error)
	RecordScheduleGateEval(ctx context.Context, id, slot, nextRun, evalAt, verdict string) (bool, error)
}

// withGate arms the scheduler's gate step. Without it a gated schedule never fires (fail closed).
func (sc *scheduler) withGate(st schedGateStore, r schedGateRunner) *scheduler {
	sc.gateStore, sc.gateRunner = st, r
	return sc
}

// gateStep runs the gate of a due schedule and reports whether the fire goes on. When it does,
// the returned schedule carries the refs for the prompt and the claim is already written: the
// ledger and the fingerprint advance first and the session is injected after, so a crash in
// between loses that one fire (at-most-once) and never fires it twice. Every other outcome is
// recorded on the row and returns false; none of them writes a run row except the first error of
// a streak, which is also the only one notified.
func (sc *scheduler) gateStep(ctx context.Context, sch store.Schedule, slot, now time.Time) (store.Schedule, bool) {
	nowRFC := now.UTC().Format(time.RFC3339)
	next, _, cerr := advanceNextRun(sch, now)
	if cerr != nil {
		log.Printf("scheduler: gated schedule %s advance: %v (not firing)", sch.ID, cerr)
		return sch, false
	}
	record := func(verdict string) bool {
		if sc.gateStore == nil {
			return false
		}
		ok, err := sc.gateStore.RecordScheduleGateEval(ctx, sch.ID, sch.NextRun, next, nowRFC, verdict)
		if err != nil {
			log.Printf("scheduler: gated schedule %s: record %q: %v", sch.ID, verdict, err)
			return false
		}
		return ok
	}
	fail := func(err error) {
		msg := truncStatus(err.Error())
		if !record(gateVerdictErrorPfx + msg) {
			return
		}
		// One notice per streak: the stored verdict is the previous evaluation's.
		if strings.HasPrefix(sch.GateLastVerdict, gateVerdictErrorPfx) {
			return
		}
		status := "error:gate:" + msg
		run := store.ScheduleRun{ID: store.NewID(), ScheduleID: sch.ID, MembershipID: sch.MembershipID, FiredAt: nowRFC,
			Status: status, Trigger: "scheduled", Slot: slot.UTC().Format(time.RFC3339)}
		if err := sc.store.AppendScheduleRun(ctx, run, scheduleRunKeep); err != nil {
			log.Printf("scheduler: append run %s: %v", sch.ID, err)
		}
		sc.notifyOutcome(ctx, sch, slot, status, nowRFC)
	}

	if sc.gateRunner == nil || sc.gateStore == nil {
		fail(errors.New("gate evaluation is not available on this Control Plane"))
		return sch, false
	}
	if sch.GateKind != gateKindWorkItems || sch.SessionMode != "new" {
		fail(fmt.Errorf("gate kind %q is not supported for session_mode %q", sch.GateKind, sch.SessionMode))
		return sch, false
	}
	spec, perr := parseGateSpec(sch.GateSpec)
	if perr != nil {
		fail(perr)
		return sch, false
	}
	items, err := sc.gateRunner.runGate(ctx, sch)
	switch {
	case errors.Is(err, errGateBypass):
		return sch, true
	case errors.Is(err, errGateNotRunning):
		record(gateVerdictNotRunning)
		return sch, false
	case errors.Is(err, errGateUnreachable):
		record(gateVerdictUnreached)
		return sch, false
	case err != nil:
		fail(err)
		return sch, false
	}
	d := gateVerdict(items, spec, gateStateOf(sch), now)
	if d.Verdict != gateVerdictFire {
		record(d.Verdict)
		return sch, false
	}
	claimed, err := sc.gateStore.ClaimScheduleGateFire(ctx, store.ScheduleGateClaim{
		ID: sch.ID, Slot: sch.NextRun, PrevFingerprint: sch.GateFiredFingerprint,
		Fingerprint: d.Fingerprint, NextRun: next, FiredAt: nowRFC, Verdict: gateVerdictFire,
		WindowStart: d.WindowStart.UTC().Format(time.RFC3339), WindowCount: d.WindowCount,
	})
	if err != nil {
		log.Printf("scheduler: gated schedule %s: claim: %v (not firing)", sch.ID, err)
		return sch, false
	}
	if !claimed {
		log.Printf("scheduler: gated schedule %s: slot already claimed or the row changed — not firing", sch.ID)
		return sch, false
	}
	sch.GateRefs = d.Refs
	return sch, true
}

// gateQueryReader is the part of the store the work-items gate reads.
type gateQueryReader interface {
	GetWorkItemQuery(ctx context.Context, id string) (store.WorkItemQuery, bool, error)
}

// evaluateWorkItemsGate resolves the gate's query through the running Agent. It never wakes a
// workspace: a runtime that is not running is errGateNotRunning. The query must still belong to
// the schedule's owner, so a spec naming someone else's query reads as an error.
func evaluateWorkItemsGate(ctx context.Context, rt runtime.Runtime, qs gateQueryReader, sch store.Schedule) ([]gateItem, error) {
	spec, err := parseGateSpec(sch.GateSpec)
	if err != nil {
		return nil, err
	}
	q, ok, err := qs.GetWorkItemQuery(ctx, spec.QueryID)
	if err != nil {
		return nil, fmt.Errorf("read work item query: %w", err)
	}
	if !ok || q.MembershipID != sch.MembershipID {
		return nil, errors.New("the work item query of this gate no longer exists")
	}
	if rt.State(ctx) != "running" {
		return nil, errGateNotRunning
	}
	rows, errs, _, err := fetchWorkItemsFromAgent(ctx, rt, []store.WorkItemQuery{q})
	if err != nil {
		var he *httpStatusError
		if errors.As(err, &he) {
			// The Agent answered and said no (an older Agent without the route, a refusal): a
			// standing problem the member should hear about.
			return nil, fmt.Errorf("%s (HTTP %d)", he.msg, he.code)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, errGateUnreachable
	}
	if msg := errs[q.ID]; msg != "" {
		return nil, fmt.Errorf("query failed: %s", msg)
	}
	items := make([]gateItem, 0, len(rows[q.ID]))
	for _, w := range rows[q.ID] {
		items = append(items, gateItem{Key: w.Key, UpdatedAt: w.UpdatedAt})
	}
	return items, nil
}

// runGate is wakeFirer's gate runner: the same membership resolution a fire makes, then the
// check against that workspace's Agent.
func (f *wakeFirer) runGate(ctx context.Context, sch store.Schedule) ([]gateItem, error) {
	identityID, ok, err := f.mgr.store.IdentityIDForMembership(ctx, sch.MembershipID)
	if err != nil {
		return nil, fmt.Errorf("resolve identity: %w", err)
	}
	if !ok {
		return nil, errGateBypass
	}
	res, aerr := f.mgr.resolveByMembership(ctx, identityID, sch.MembershipID)
	if aerr != nil {
		if aerr.code == "forbidden_tenant" {
			return nil, errGateBypass
		}
		return nil, fmt.Errorf("resolve membership: %s", aerr.message)
	}
	return evaluateWorkItemsGate(ctx, res.rt, f.mgr.store, sch)
}

// --- the API side ------------------------------------------------------------------

// scheduleGateDTO is the gate on the schedule wire. Kind "" or "none" on a patch removes the
// gate. The last three fields are output only.
type scheduleGateDTO struct {
	Kind           string `json:"kind"`
	QueryID        string `json:"query_id,omitempty"`
	Fingerprint    string `json:"fingerprint,omitempty"`
	MaxFiresPerDay int    `json:"max_fires_per_day,omitempty"`
	MinFireGapS    int    `json:"min_fire_gap_s,omitempty"`
	FiredAt        string `json:"fired_at,omitempty"`
	LastEvalAt     string `json:"last_eval_at,omitempty"`
	LastVerdict    string `json:"last_verdict,omitempty"`
}

// scheduleGateToDTO renders the stored gate, or nil for a schedule without one.
func scheduleGateToDTO(s store.Schedule) *scheduleGateDTO {
	if s.GateKind == "" {
		return nil
	}
	out := &scheduleGateDTO{Kind: s.GateKind, FiredAt: s.GateFiredAt, LastEvalAt: s.GateLastEvalAt, LastVerdict: s.GateLastVerdict}
	if spec, err := parseGateSpec(s.GateSpec); err == nil {
		out.QueryID, out.Fingerprint, out.MaxFiresPerDay, out.MinFireGapS = spec.QueryID, spec.Fingerprint, spec.MaxFiresPerDay, spec.MinFireGapS
	}
	return out
}

// applyGateDTO validates a requested gate against the schedule it is for and writes it into
// sch: kind and the normalized spec. The query must belong to the member, and a gate is for
// recurring new-session schedules only: reuse and assistant fires have no per-slot idempotency
// to lean on, and a once has nothing to poll.
func applyGateDTO(ctx context.Context, qs gateQueryReader, sch *store.Schedule, in scheduleGateDTO) *apiError {
	if in.Kind == "" || in.Kind == "none" {
		sch.GateKind, sch.GateSpec = "", ""
		return nil
	}
	if in.Kind != gateKindWorkItems {
		return &apiError{http.StatusBadRequest, "bad_gate", "gate.kind must be work_items or none"}
	}
	spec := gateWorkItemsSpec{QueryID: strings.TrimSpace(in.QueryID), Fingerprint: in.Fingerprint,
		MaxFiresPerDay: in.MaxFiresPerDay, MinFireGapS: in.MinFireGapS}.withDefaults()
	if err := spec.validate(); err != nil {
		return &apiError{http.StatusBadRequest, "bad_gate", "gate: " + err.Error()}
	}
	q, ok, err := qs.GetWorkItemQuery(ctx, spec.QueryID)
	if err != nil {
		return internalErr(err)
	}
	// The same answer for a missing query and someone else's, as getOwned gives a schedule.
	if !ok || q.MembershipID != sch.MembershipID {
		return &apiError{http.StatusBadRequest, "bad_gate", "gate.query_id is not one of your saved work item queries"}
	}
	b, _ := json.Marshal(spec)
	sch.GateKind, sch.GateSpec = gateKindWorkItems, string(b)
	return nil
}

// checkGateFits refuses a gate on a schedule it cannot drive.
func checkGateFits(sch store.Schedule) *apiError {
	if sch.GateKind == "" {
		return nil
	}
	if sch.SessionMode != "new" {
		return &apiError{http.StatusBadRequest, "bad_gate", "a gate applies to session_mode new only"}
	}
	if sch.SpecKind == "once" {
		return &apiError{http.StatusBadRequest, "bad_gate", "a gate applies to cron and interval schedules, not once"}
	}
	return nil
}

// gateRefsText is the value of {{gate.refs}}.
func gateRefsText(sch store.Schedule) string { return strings.Join(sch.GateRefs, ", ") }
