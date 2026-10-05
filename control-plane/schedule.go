package main

// schedule.go — scheduled-execution operator API (docs/log/38 + ADR0021 P3). The handlers
// are the CP-native face the in-container operator reaches through the /internal/schedules
// bridge (schedule_bridge.go, AF_SCHEDULE_TOKEN). Definitions live in the CP DB because
// the CP is the only thing alive while a workspace is stopped. The scheduler goroutine
// (scheduler.go) reads them; these handlers are the write/inspect side.
//
// The NL->spec translation is the operator LLM's job (ADR0021 decision 7): the create/
// update tools receive a STRUCTURED spec (spec_kind + spec + tz) and this API validates
// it and computes the concrete next fire, which the operator reads back to the user.

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

type scheduleAPI struct {
	memberAuth
	store store.ScheduleStore
}

func newScheduleAPI(m *manager) scheduleAPI { return scheduleAPI{memberAuth{m}, m.store} }

// scheduleDTO is the wire shape for a schedule (create input + list/get output). Snake
// case matches the MCP tool args, forwarded verbatim as the request body.
type scheduleDTO struct {
	ID            string `json:"id,omitempty"`
	SpecKind      string `json:"spec_kind"`
	Spec          string `json:"spec"`
	SpecLabel     string `json:"spec_label,omitempty"`
	TZ            string `json:"tz,omitempty"`
	WakePolicy    string `json:"wake_policy,omitempty"`
	SessionMode   string `json:"session_mode,omitempty"`
	ReuseTarget   string `json:"reuse_target,omitempty"`
	AgentKind     string `json:"agent_kind,omitempty"`
	Model         string `json:"model,omitempty"`
	Repo          string `json:"repo,omitempty"`
	Worktree      string `json:"worktree,omitempty"`
	NewBranch     bool   `json:"new_branch,omitempty"`
	Prompt        string `json:"prompt,omitempty"`
	OverlapPolicy string `json:"overlap_policy,omitempty"`
	// Reuse (P6): Rotation is the JSON rotation policy blob, MissingTargetPolicy governs a
	// pinned reuse whose target is gone. The reuse ledger (ReuseSession/ReuseRunCount) is
	// read-only output so the operator/Console can show which session is current.
	Rotation            string `json:"rotation,omitempty"`
	MissingTargetPolicy string `json:"missing_target_policy,omitempty"`
	ReuseSession        string `json:"reuse_session,omitempty"`
	ReuseRunCount       int    `json:"reuse_run_count,omitempty"`
	OwnerConv           string `json:"owner_conv,omitempty"`
	// Report opts the fire's session into the docs/log/30 completion report back to the
	// owner conversation. Default false = no report (the fire runs silently).
	Report bool `json:"report"`
	// StopAfterRun folds the fire's session away once it has finished the prompt
	// (docs/log/85). Default false = leave it running. Ignored in session_mode=assistant,
	// which drives a conversation and holds no session.
	StopAfterRun bool `json:"stop_after_run"`
	// DeliverTo is where a fire's result goes when Report is on (issue #1560): any of
	// operator, notifications, discord, slack. Empty on input means operator alone, today's
	// report; the output always names the resolved list.
	DeliverTo []string `json:"deliver_to"`
	// Silent lets an answer that is exactly scheduleSilentSentinel deliver nothing.
	Silent       bool   `json:"silent"`
	Enabled      bool   `json:"enabled"`
	NextRun      string `json:"next_run,omitempty"`
	NextRunLocal string `json:"next_run_local,omitempty"` // next_run rendered in the schedule's tz
	LastRun      string `json:"last_run,omitempty"`
	LastStatus   string `json:"last_status,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
	// Warning is set on a create/run_now response when the scheduler goroutine is not
	// running on this deployment — the schedule is stored but will never fire until an
	// operator enables it (AF_SCHEDULER_INTERVAL). Empty otherwise. The operator relays it.
	Warning string `json:"warning,omitempty"`
}

// withSchedulerWarning stamps the "scheduler disabled" note onto a DTO when the scheduler
// goroutine is not running, so create/run_now do not silently succeed on a deployment that
// never fires anything. A no-op when the scheduler is enabled.
func withSchedulerWarning(d scheduleDTO) scheduleDTO {
	if !schedulerRunning {
		d.Warning = "このデプロイでは定時実行スケジューラが無効（AF_SCHEDULER_INTERVAL 未設定）のため、登録しても発火しません。運用者に有効化を依頼してください。"
	}
	return d
}

func scheduleToDTO(s store.Schedule) scheduleDTO {
	d := scheduleDTO{
		ID: s.ID, SpecKind: s.SpecKind, Spec: s.Spec, SpecLabel: s.SpecLabel, TZ: s.TZ,
		WakePolicy: s.WakePolicy, SessionMode: s.SessionMode, ReuseTarget: s.ReuseTarget,
		AgentKind: s.AgentKind, Model: s.Model, Repo: s.Repo, Worktree: s.Worktree,
		NewBranch: s.NewBranch, Prompt: s.Prompt, OverlapPolicy: s.OverlapPolicy,
		Rotation: s.Rotation, MissingTargetPolicy: s.MissingTargetPolicy,
		ReuseSession: s.ReuseSession, ReuseRunCount: s.ReuseRunCount,
		OwnerConv: s.OwnerConv, Report: s.Report, StopAfterRun: s.StopAfterRun,
		DeliverTo: scheduleTargets(s.DeliverTo), Silent: s.Silent, Enabled: s.Enabled, NextRun: s.NextRun, LastRun: s.LastRun,
		LastStatus: s.LastStatus, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
	// Render the next fire in the schedule's own zone so the operator can read back a
	// human-friendly "next: 2026-07-23 09:00 JST" for confirmation (ADR0021 §④'').
	if t, err := time.Parse(time.RFC3339, s.NextRun); err == nil {
		d.NextRunLocal = t.In(scheduleLocation(s.TZ)).Format("2006-01-02 15:04 MST")
	}
	return d
}

// applyScheduleDefaults fills the optional policy fields so the operator can omit them.
func applyScheduleDefaults(s *store.Schedule) {
	if s.TZ == "" {
		s.TZ = "UTC"
	}
	if s.WakePolicy == "" {
		s.WakePolicy = "wake"
	}
	if s.SessionMode == "" {
		s.SessionMode = "new"
	}
	if s.AgentKind == "" {
		s.AgentKind = "claude"
	}
	if s.OverlapPolicy == "" {
		s.OverlapPolicy = "skip"
	}
	if s.MissingTargetPolicy == "" {
		s.MissingTargetPolicy = "recreate"
	}
}

// validateScheduleDTO normalizes + validates a create input into a Schedule (id/ledger
// unset). It enforces the enum fields and the spec/tz, and requires a prompt.
func validateScheduleDTO(mv store.MembershipView, in scheduleDTO) (store.Schedule, *apiError) {
	s := store.Schedule{
		MembershipID: mv.MembershipID, TenantID: mv.TenantID,
		OwnerConv: strings.TrimSpace(in.OwnerConv),
		SpecKind:  strings.TrimSpace(in.SpecKind), Spec: strings.TrimSpace(in.Spec),
		SpecLabel: strings.TrimSpace(in.SpecLabel), TZ: strings.TrimSpace(in.TZ),
		WakePolicy: strings.TrimSpace(in.WakePolicy), SessionMode: strings.TrimSpace(in.SessionMode),
		ReuseTarget: strings.TrimSpace(in.ReuseTarget), AgentKind: strings.TrimSpace(in.AgentKind),
		Model: strings.TrimSpace(in.Model), Repo: strings.TrimSpace(in.Repo),
		Worktree: strings.TrimSpace(in.Worktree), NewBranch: in.NewBranch,
		Prompt: in.Prompt, OverlapPolicy: strings.TrimSpace(in.OverlapPolicy),
		Rotation: strings.TrimSpace(in.Rotation), MissingTargetPolicy: strings.TrimSpace(in.MissingTargetPolicy),
		Report: in.Report, StopAfterRun: in.StopAfterRun, Silent: in.Silent,
	}
	targets, aerr := normalizeScheduleTargets(in.DeliverTo)
	if aerr != nil {
		return store.Schedule{}, aerr
	}
	s.DeliverTo = targets
	applyScheduleDefaults(&s)
	if strings.TrimSpace(s.Prompt) == "" {
		return store.Schedule{}, &apiError{http.StatusBadRequest, "bad_prompt", "prompt is required"}
	}
	if aerr := validateScheduleFields(s); aerr != nil {
		return store.Schedule{}, aerr
	}
	return s, nil
}

// validateScheduleFields checks the enums + spec/tz shared by create and update.
func validateScheduleFields(s store.Schedule) *apiError {
	if err := validateSpec(s.SpecKind, s.Spec, s.TZ); err != nil {
		return &apiError{http.StatusBadRequest, "bad_spec", err.Error()}
	}
	if !oneOf(s.WakePolicy, "wake", "skip", "catch_up") {
		return &apiError{http.StatusBadRequest, "bad_wake_policy", "wake_policy must be wake, skip, or catch_up"}
	}
	if !oneOf(s.OverlapPolicy, "skip", "queue", "restart") {
		return &apiError{http.StatusBadRequest, "bad_overlap_policy", "overlap_policy must be skip, queue, or restart"}
	}
	// "assistant" (docs/log/38, assistant fire): the fire runs one assistant-chat turn in a
	// conversation (reuse_target = "a…" slug / UUID, empty = the schedule's owner_conv)
	// instead of driving a session. repo/agent_kind/model are ignored in that mode —
	// the conversation carries its own provider/model/persona.
	if !oneOf(s.SessionMode, "new", "reuse", "assistant") {
		return &apiError{http.StatusBadRequest, "bad_session_mode", "session_mode must be new, reuse, or assistant"}
	}
	if s.MissingTargetPolicy != "" && !oneOf(s.MissingTargetPolicy, "recreate", "fail") {
		return &apiError{http.StatusBadRequest, "bad_missing_target_policy", "missing_target_policy must be recreate or fail"}
	}
	if err := validateRotation(s.Rotation); err != nil {
		return &apiError{http.StatusBadRequest, "bad_rotation", err.Error()}
	}
	// An assistant fire's result is the turn it adds to the conversation itself, so there is
	// nothing for a target list or the sentinel to route.
	if s.SessionMode == "assistant" && (s.Silent || (s.DeliverTo != "" && s.DeliverTo != scheduleTargetOperator)) {
		return &apiError{http.StatusBadRequest, "bad_delivery", "deliver_to and silent apply to session_mode new and reuse only"}
	}
	return nil
}

// Delivery targets of a schedule's result (issue #1560). Only destinations that already
// exist: the owner's operator conversation, the notification center, and the member's own
// chat-bridge connections. The Agent checks a bridge's connection and identity binding at
// delivery time, because the connection lives in the member's workspace, not here.
const (
	scheduleTargetOperator      = "operator"
	scheduleTargetNotifications = "notifications"
	scheduleTargetDiscord       = "discord"
	scheduleTargetSlack         = "slack"
)

// scheduleSilentSentinel is the whole final answer that marks a run with nothing to say.
// Compared trimmed and case-sensitive, by the Agent, which sees the answer.
const scheduleSilentSentinel = "[SILENT]"

// normalizeScheduleTargets validates a target list and stores it in a fixed order, so the
// same choice is always the same column value. An empty list stays empty (= operator).
func normalizeScheduleTargets(in []string) (string, *apiError) {
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.TrimSpace(t)
		if !oneOf(t, scheduleTargetOperator, scheduleTargetNotifications, scheduleTargetDiscord, scheduleTargetSlack) {
			return "", &apiError{http.StatusBadRequest, "bad_deliver_to", "deliver_to entries must be operator, notifications, discord, or slack"}
		}
		seen[t] = true
	}
	var out []string
	for _, t := range []string{scheduleTargetOperator, scheduleTargetNotifications, scheduleTargetDiscord, scheduleTargetSlack} {
		if seen[t] {
			out = append(out, t)
		}
	}
	return strings.Join(out, ","), nil
}

// scheduleTargets is the resolved target list of a stored column value: empty is operator.
func scheduleTargets(col string) []string {
	if col == "" {
		return []string{scheduleTargetOperator}
	}
	return strings.Split(col, ",")
}

// scheduleHasTarget reports whether the schedule's resolved targets include t.
func scheduleHasTarget(sch store.Schedule, t string) bool {
	return oneOf(t, scheduleTargets(sch.DeliverTo)...)
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// getOwned fetches a schedule and asserts the caller owns it (404 otherwise, so a
// foreign id is indistinguishable from a missing one).
func (a scheduleAPI) getOwned(r *http.Request, id string, mv store.MembershipView) (store.Schedule, *apiError) {
	sch, ok, err := a.store.GetSchedule(r.Context(), id)
	if err != nil {
		return store.Schedule{}, internalErr(err)
	}
	if !ok || sch.MembershipID != mv.MembershipID {
		return store.Schedule{}, &apiError{http.StatusNotFound, "not_found", "schedule not found"}
	}
	return sch, nil
}

// --- handlers -------------------------------------------------------------------

func (a scheduleAPI) list(w http.ResponseWriter, r *http.Request, mv store.MembershipView) {
	rows, err := a.store.ListSchedules(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]scheduleDTO, 0, len(rows))
	for _, s := range rows {
		out = append(out, scheduleToDTO(s))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a scheduleAPI) create(w http.ResponseWriter, r *http.Request, mv store.MembershipView) {
	var in scheduleDTO
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	s, aerr := validateScheduleDTO(mv, in)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	next, err := initialNextRun(s, time.Now().UTC())
	if err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_spec", err.Error()})
		return
	}
	s.ID = "sch_" + store.NewID()
	s.Enabled = true
	s.NextRun = next
	s.CreatedAt = store.NowTS()
	s.UpdatedAt = s.CreatedAt
	if err := a.store.CreateSchedule(r.Context(), s); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusCreated, withSchedulerWarning(scheduleToDTO(s)))
}

// update patches a schedule. Nil pointer fields are left unchanged; when the spec or tz
// changes the next fire is recomputed so the ledger stays consistent with the new spec.
func (a scheduleAPI) update(w http.ResponseWriter, r *http.Request, mv store.MembershipView) {
	id := r.PathValue("id")
	sch, aerr := a.getOwned(r, id, mv)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	var p schedulePatch
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	oldReuseTarget, oldSessionMode := sch.ReuseTarget, sch.SessionMode
	specChanged, aerr := p.apply(&sch)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	applyScheduleDefaults(&sch)
	// Re-pin (P6): an edited reuse_target / session_mode must actually take effect.
	// The adopted-session ledger otherwise keeps pointing at the old session for as
	// long as it lives, and fires never move to the new target.
	reuseChanged := (p.ReuseTarget != nil && sch.ReuseTarget != oldReuseTarget) ||
		(p.SessionMode != nil && sch.SessionMode != oldSessionMode)
	if aerr := validateScheduleFields(sch); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if strings.TrimSpace(sch.Prompt) == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_prompt", "prompt is required"})
		return
	}
	// Recompute the next fire when the schedule is enabled and its timing changed, so an
	// edited cron/interval/once takes effect on the right instant.
	if specChanged && sch.Enabled {
		next, err := initialNextRun(sch, time.Now().UTC())
		if err != nil {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_spec", err.Error()})
			return
		}
		sch.NextRun = next
	}
	sch.UpdatedAt = store.NowTS()
	if err := a.store.UpdateSchedule(r.Context(), sch); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if reuseChanged {
		if err := a.store.SetScheduleReuse(r.Context(), sch.ID, "", "", 0, sch.UpdatedAt); err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		sch.ReuseSession, sch.ReuseStartedAt, sch.ReuseRunCount = "", "", 0
	}
	writeJSON(w, http.StatusOK, scheduleToDTO(sch))
}

func (a scheduleAPI) delete(w http.ResponseWriter, r *http.Request, mv store.MembershipView) {
	id := r.PathValue("id")
	if _, aerr := a.getOwned(r, id, mv); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if err := a.store.DeleteSchedule(r.Context(), id, mv.MembershipID); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

func (a scheduleAPI) pause(w http.ResponseWriter, r *http.Request, mv store.MembershipView) {
	id := r.PathValue("id")
	if _, aerr := a.getOwned(r, id, mv); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	// Clear next_run so a paused schedule is never due (the due query also filters
	// enabled=1, but an empty next_run makes the paused state explicit).
	if err := a.store.SetScheduleEnabled(r.Context(), id, mv.MembershipID, false, "", store.NowTS()); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	a.writeOne(w, r, id, mv)
}

func (a scheduleAPI) resume(w http.ResponseWriter, r *http.Request, mv store.MembershipView) {
	id := r.PathValue("id")
	sch, aerr := a.getOwned(r, id, mv)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	// A spent `once` whose instant is in the past must not be resumed: initialNextRun
	// would return that past instant and it would fire again immediately. Reject so the
	// operator creates a fresh schedule instead of silently re-running a one-shot.
	if sch.SpecKind == "once" {
		if t, perr := parseOnce(sch.Spec); perr == nil && !t.After(time.Now().UTC()) {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "once_in_past",
				"この once スケジュールは指定時刻を過ぎているため再開できません。新しく作成してください"})
			return
		}
	}
	next, err := initialNextRun(sch, time.Now().UTC())
	if err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_spec", err.Error()})
		return
	}
	if err := a.store.SetScheduleEnabled(r.Context(), id, mv.MembershipID, true, next, store.NowTS()); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	a.writeOne(w, r, id, mv)
}

// runNow marks a schedule due immediately by setting next_run to now, so the scheduler
// fires it on its next tick through the SAME path as a timed fire (wake policy, keep-
// alive, idempotency) — ADR0021 decision 8. It does not fire inline (a cold wake could
// exceed the bridge request timeout). Requires the scheduler goroutine to be enabled and
// the schedule to be enabled (a paused schedule must be resumed first).
func (a scheduleAPI) runNow(w http.ResponseWriter, r *http.Request, mv store.MembershipView) {
	id := r.PathValue("id")
	sch, aerr := a.getOwned(r, id, mv)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if !sch.Enabled {
		writeAPIErr(w, &apiError{http.StatusConflict, "schedule_paused", "schedule is paused; resume it before run_now"})
		return
	}
	// Set next_run to now MINUS the schedule's fire jitter so the tick's jitter gate
	// (now >= next_run+jitter) passes immediately — run_now is a "fire now" affordance and
	// must not inherit the cron spread delay. For interval/once jitter is 0, so this is now.
	now := time.Now().UTC()
	next := now.Add(-jitterForSchedule(sch)).Format(time.RFC3339)
	// Flag manual_fire_pending too so the resulting run history row is tagged as a manual
	// run-now rather than an automatic scheduled fire (both go through the same ticker path).
	if err := a.store.MarkManualFirePending(r.Context(), id, mv.MembershipID, next, store.NowTS()); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// Re-read and return, surfacing the disabled-scheduler warning: run_now on a
	// deployment with no scheduler goroutine would otherwise report success yet never fire.
	sch, aerr = a.getOwned(r, id, mv)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, withSchedulerWarning(scheduleToDTO(sch)))
}

func (a scheduleAPI) runs(w http.ResponseWriter, r *http.Request, mv store.MembershipView) {
	id := r.PathValue("id")
	if _, aerr := a.getOwned(r, id, mv); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	rows, err := a.store.ListScheduleRuns(r.Context(), id, mv.MembershipID, 50)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, rn := range rows {
		out = append(out, map[string]any{
			"fired_at": rn.FiredAt, "status": rn.Status, "detail": rn.Detail,
			"session": rn.Session, "trigger": rn.Trigger,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedule_id": id, "runs": out})
}

// runNotExecuted records that a run the scheduler recorded as fired never ran (#1257): a
// Managed session queued its prompt behind a running turn and dropped it before it started
// (an archive, a discarding stop, a removal). The Agent learns of the drop long after the fire,
// so it names the run by schedule, session and slot. The run's status becomes an error, which
// the history shows as a failure, and the member is notified as for a failed fire.
func (a scheduleAPI) runNotExecuted(w http.ResponseWriter, r *http.Request, mv store.MembershipView) {
	id := r.PathValue("id")
	sch, aerr := a.getOwned(r, id, mv)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	var body struct {
		Session string `json:"session"`
		Slot    string `json:"slot"`
		Reason  string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_body", "invalid JSON body"})
		return
	}
	slot, err := time.Parse(time.RFC3339, body.Slot)
	if err != nil || strings.TrimSpace(body.Session) == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_body", "session and an RFC 3339 slot are required"})
		return
	}
	reason := truncStatus(body.Reason)
	if reason == "" {
		reason = "dropped"
	}
	status := truncStatus("error:not executed: the queued prompt was dropped before it ran (" + reason + ")")
	found, changed, err := a.store.MarkScheduleRunNotExecuted(r.Context(), id, mv.MembershipID, body.Session,
		slot.UTC().Format(time.RFC3339), status, reason)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found {
		writeAPIErr(w, &apiError{http.StatusNotFound, "run_not_found", "no run of this schedule for that session and slot"})
		return
	}
	if changed {
		notifyScheduleNotExecuted(r.Context(), a.mgr.store, sch, slot, body.Session, status)
	}
	writeJSON(w, http.StatusOK, scheduleRunNotExecutedResp{ScheduleID: id, Status: status, Changed: changed})
}

// runSilent records that a run answered with the silent sentinel and so delivered nothing
// (#1560). The Agent decides it, because only the Agent sees the answer; the run keeps its
// place in the history as fired_silent so a quiet monitor still shows that it ran. Nothing
// is notified: saying nothing is what the schedule asked for.
func (a scheduleAPI) runSilent(w http.ResponseWriter, r *http.Request, mv store.MembershipView) {
	id := r.PathValue("id")
	if _, aerr := a.getOwned(r, id, mv); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	var body struct {
		Session string `json:"session"`
		Slot    string `json:"slot"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_body", "invalid JSON body"})
		return
	}
	slot, err := time.Parse(time.RFC3339, body.Slot)
	if err != nil || strings.TrimSpace(body.Session) == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_body", "session and an RFC 3339 slot are required"})
		return
	}
	found, changed, err := a.store.MarkScheduleRunSilent(r.Context(), id, mv.MembershipID, body.Session,
		slot.UTC().Format(time.RFC3339))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found {
		writeAPIErr(w, &apiError{http.StatusNotFound, "run_not_found", "no run of this schedule for that session and slot"})
		return
	}
	resp := scheduleRunNotExecutedResp{ScheduleID: id, Changed: changed}
	if changed {
		resp.Status = store.ScheduleStatusFiredSilent
	}
	writeJSON(w, http.StatusOK, resp)
}

// scheduleRunNotExecutedResp is runNotExecuted's (and runSilent's) answer. Changed is false for a repeated
// report, or a run that had already failed: nothing was written.
type scheduleRunNotExecutedResp struct {
	ScheduleID string `json:"schedule_id"`
	Status     string `json:"status"`
	Changed    bool   `json:"changed"`
}

// notifyScheduleNotExecuted is notifyOutcome's notification for a run found not executed after
// the fact. The EventID names the session too: one slot of a reuse schedule has one run, but a
// new-mode schedule's create is retried under the same slot.
func notifyScheduleNotExecuted(ctx context.Context, st interface {
	InsertNotification(context.Context, store.Notification) error
}, sch store.Schedule, slot time.Time, session, status string) {
	label := sch.SpecLabel
	if label == "" {
		label = sch.ID
	}
	payload, _ := json.Marshal(map[string]any{
		"schedule_id": sch.ID, "status": status, "spec_label": sch.SpecLabel, "spec": sch.Spec,
	})
	n := store.Notification{
		EventID:      "sched-not-executed-" + sch.ID + "-" + slot.UTC().Format(time.RFC3339) + "-" + session,
		MembershipID: sch.MembershipID, Kind: "schedule-failed",
		TargetType: "schedule", TargetID: sch.ID, TargetKind: sch.AgentKind,
		DisplayName: label, Payload: string(payload), CreatedAt: store.NowTS(),
	}
	if err := st.InsertNotification(ctx, n); err != nil {
		log.Printf("schedule: notify not executed %s: %v", sch.ID, err)
	}
}

// writeOne re-reads and returns a schedule DTO — the shared tail of the toggle handlers
// so the operator sees the resulting state (including the recomputed next_run).
func (a scheduleAPI) writeOne(w http.ResponseWriter, r *http.Request, id string, mv store.MembershipView) {
	sch, aerr := a.getOwned(r, id, mv)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, scheduleToDTO(sch))
}

// schedulePatch carries partial edits (nil = unchanged), mirroring memoPatch.
type schedulePatch struct {
	SpecKind            *string   `json:"spec_kind"`
	Spec                *string   `json:"spec"`
	SpecLabel           *string   `json:"spec_label"`
	TZ                  *string   `json:"tz"`
	WakePolicy          *string   `json:"wake_policy"`
	SessionMode         *string   `json:"session_mode"`
	ReuseTarget         *string   `json:"reuse_target"`
	AgentKind           *string   `json:"agent_kind"`
	Model               *string   `json:"model"`
	Repo                *string   `json:"repo"`
	Worktree            *string   `json:"worktree"`
	NewBranch           *bool     `json:"new_branch"`
	Prompt              *string   `json:"prompt"`
	OverlapPolicy       *string   `json:"overlap_policy"`
	Rotation            *string   `json:"rotation"`
	MissingTargetPolicy *string   `json:"missing_target_policy"`
	Report              *bool     `json:"report"`
	StopAfterRun        *bool     `json:"stop_after_run"`
	DeliverTo           *[]string `json:"deliver_to"`
	Silent              *bool     `json:"silent"`
	// owner_conv is intentionally NOT patchable: create stamps it to the operator's own
	// conversation (mcp_stdio withOwnerConv) so completion reports always return to the
	// operator. Letting update change it would let a report be redirected within the
	// membership, so it is fixed for the schedule's lifetime.
}

// apply overlays the non-nil fields onto sch and reports whether the timing (spec_kind/
// spec/tz) changed, so the caller knows to recompute next_run. A malformed deliver_to is
// the one field it rejects itself, since only it is normalized on the way in.
func (p schedulePatch) apply(sch *store.Schedule) (specChanged bool, aerr *apiError) {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	if p.SpecKind != nil {
		sch.SpecKind = strings.TrimSpace(*p.SpecKind)
		specChanged = true
	}
	if p.Spec != nil {
		sch.Spec = strings.TrimSpace(*p.Spec)
		specChanged = true
	}
	if p.TZ != nil {
		sch.TZ = strings.TrimSpace(*p.TZ)
		specChanged = true
	}
	set(&sch.SpecLabel, p.SpecLabel)
	set(&sch.WakePolicy, p.WakePolicy)
	set(&sch.SessionMode, p.SessionMode)
	set(&sch.ReuseTarget, p.ReuseTarget)
	set(&sch.OverlapPolicy, p.OverlapPolicy)
	set(&sch.Rotation, p.Rotation)
	set(&sch.MissingTargetPolicy, p.MissingTargetPolicy)
	set(&sch.AgentKind, p.AgentKind)
	set(&sch.Model, p.Model)
	set(&sch.Repo, p.Repo)
	set(&sch.Worktree, p.Worktree)
	if p.Prompt != nil {
		sch.Prompt = *p.Prompt // prompt kept verbatim (leading/trailing space may matter)
	}
	if p.NewBranch != nil {
		sch.NewBranch = *p.NewBranch
	}
	if p.Report != nil {
		sch.Report = *p.Report
	}
	if p.StopAfterRun != nil {
		sch.StopAfterRun = *p.StopAfterRun
	}
	if p.DeliverTo != nil {
		targets, aerr := normalizeScheduleTargets(*p.DeliverTo)
		if aerr != nil {
			return specChanged, aerr
		}
		sch.DeliverTo = targets
	}
	if p.Silent != nil {
		sch.Silent = *p.Silent
	}
	return specChanged, nil
}
