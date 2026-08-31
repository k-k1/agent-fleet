package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Scheduled execution P6 (docs/log/38 + ADR0021): long-lived session reuse. When a schedule
// runs with session_mode=reuse the scheduler sends each fire's prompt into the SAME
// long-lived session (send_to_session) instead of creating a fresh one, so the
// conversation context carries across fires. Two shapes:
//
//   - pinned (reuse_target set): the operator names an existing session. The scheduler
//     sends there; the session's lifecycle is the user's. Rotation does not apply (we do
//     not retire a session the user owns). If the target is gone, missing_target_policy
//     decides recreate-and-adopt vs fail-and-notify.
//   - managed (reuse_target empty): the schedule owns a dedicated session it creates on
//     the first fire and rotates per the rotation policy (every_runs / after / calendar).
//
// The reuse ledger (reuse_session / reuse_started_at / reuse_run_count) tracks which real
// session is current, when it began, and its fire count since the last rotation. kind /
// model / dir come from the EXISTING session (not the schedule) once it exists, so a
// driver switch (★5 busy_switch) never happens in reuse — we never recreate to change it.

// fireReuse is the reuse counterpart of the new-session inject in fire(). It runs after
// the workspace is awake, the keep-alive is held, and the Agent is reachable, so it only
// has to pick/rotate the target session and deliver the prompt. It persists the reuse
// ledger itself (SetScheduleReuse); fireOne stamps the cron ledger afterward.
func (f *wakeFirer) fireReuse(ctx context.Context, res *resolved, sch Schedule, slot time.Time) (string, string, error) {
	sessions, err := f.mgr.agentSessions(ctx, res.rt)
	if err != nil {
		return "", "", fmt.Errorf("list sessions: %w", err)
	}
	pinned := sch.ReuseTarget != ""
	loc := scheduleLocation(sch.TZ)

	// Effective current session name: the adopted reuse_session if we have one, else the
	// operator-named pinned target on its first fire.
	curName := sch.ReuseSession
	if curName == "" {
		curName = sch.ReuseTarget
	}
	cur := findSessionByName(sessions, curName)

	// Rotation is managed-only: never retire a user-owned pinned session.
	rotated := false
	if cur != nil && !pinned && rotationDue(sch, slot, loc) {
		f.retireSession(ctx, res.rt, cur.Name)
		cur = nil
		rotated = true
	}

	if cur == nil {
		// (Re)create is required. A pinned schedule whose target is absent honors the
		// missing-target policy before creating a replacement.
		if pinned && reuseMissingPolicy(sch) == "fail" {
			return "skipped_target_missing", "", nil
		}
		title := reuseCreateTitle(sch, pinned)
		name, cerr := f.createReuseSession(ctx, res.rt, sch, slot, title)
		if cerr != nil {
			return "", "", cerr
		}
		f.saveReuse(ctx, sch.ID, name, slot.UTC().Format(time.RFC3339), 1)
		switch {
		case rotated:
			return "fired_rotated", name, nil
		case sch.ReuseSession != "":
			// We had an adopted session before but it was gone — this is a recreate.
			return "fired_recreated", name, nil
		default:
			return "fired", name, nil
		}
	}

	// The current session exists: apply overlap policy, then deliver. Link the run to it
	// even on an overlap skip so the history can open the busy session that deferred us.
	skip, derr := f.deliverReuse(ctx, res.rt, sch, cur, slot)
	if derr != nil {
		return "", "", derr
	}
	if skip != "" {
		return skip, cur.Name, nil
	}
	started := sch.ReuseStartedAt
	if started == "" {
		started = slot.UTC().Format(time.RFC3339)
	}
	f.saveReuse(ctx, sch.ID, cur.Name, started, sch.ReuseRunCount+1)
	return "fired", cur.Name, nil
}

// deliverReuse resolves the overlap policy against the target's live state, then sends
// the prompt (resuming the session if it is stopped). A non-empty skipStatus means the
// overlap policy declined to deliver this fire (recorded, not an error).
func (f *wakeFirer) deliverReuse(ctx context.Context, rt Runtime, sch Schedule, target *sessionWire, slot time.Time) (skipStatus string, err error) {
	body := reuseSendBody(sch, slot)
	alive := target.Alive
	if target.Alive && sessionBusy(target.State) {
		switch reuseOverlap(sch) {
		case "skip":
			return "skipped_overlap", nil
		case "restart":
			// Interrupt the running turn (disarm its now-superseded report), which leaves
			// the session stopped; the send below resumes it fresh.
			halt, _ := json.Marshal(map[string]bool{"disarm_report": true})
			if _, _, herr := f.agentReq(ctx, rt, http.MethodPost, "/sessions/"+url.PathEscape(target.Name)+"/halt", halt); herr != nil {
				return "", fmt.Errorf("restart halt: %w", herr)
			}
			alive = false
		case "queue":
			// Fall through: POST /input to a working session queues it as steering.
		}
	}
	if serr := f.sendToSession(ctx, rt, target.Name, alive, body); serr != nil {
		return "", serr
	}
	return "", nil
}

// sendToSession delivers body to an existing session's /input, first confirming the
// session is actually input-ready — its composer is drawn, not a still-booting or zombie
// pane — so a typed prompt cannot silently vanish. It mirrors the Agent-side
// agentSendToSession contract (a session that stopped between the list read and the POST,
// 409 not_running, is resumed and retried) but adds the readiness gate on the ALREADY-ALIVE
// path too: /input types keystrokes into the pane and returns 200 regardless of whether
// the CLI can accept them, and the unattended cron has no human to notice a swallowed
// prompt. This is the sbk7oej silent-drop fix (docs/log/38 + ADR0021).
func (f *wakeFirer) sendToSession(ctx context.Context, rt Runtime, name string, alive bool, body []byte) error {
	inputPath := "/sessions/" + url.PathEscape(name) + "/input"
	if !alive {
		return f.resumeAndSend(ctx, rt, name, inputPath, body)
	}
	if err := f.awaitSessionReady(ctx, rt, name); err != nil {
		return err
	}
	respBody, status, err := f.agentReq(ctx, rt, http.MethodPost, inputPath, body)
	if err != nil {
		return err
	}
	if status == http.StatusConflict && respHasCode(respBody, "not_running") {
		// Stopped between the readiness check and this POST — resume and retry. Other
		// conflicts (e.g. question_pending) are real errors, not a reason to resend.
		return f.resumeAndSend(ctx, rt, name, inputPath, body)
	}
	if status >= 300 {
		return fmt.Errorf("send_to_session %d: %s", status, strings.TrimSpace(string(respBody)))
	}
	return nil
}

func (f *wakeFirer) resumeAndSend(ctx context.Context, rt Runtime, name, inputPath string, body []byte) error {
	if _, status, err := f.agentReq(ctx, rt, http.MethodPost, "/sessions/"+url.PathEscape(name)+"/start", nil); err != nil {
		return fmt.Errorf("resume: %w", err)
	} else if status >= 300 {
		return fmt.Errorf("resume %d", status)
	}
	// Wait for input-readiness (alive AND composer drawn), not mere aliveness: a freshly
	// resumed TUI has a pane before its composer is up, and a prompt typed into that boot
	// screen is lost while /input still returns 200 (the sbk7oej silent-drop — WS running,
	// session stopped, resumed at the fire). Mirrors the Agent's agentWaitSessionReady.
	if err := f.awaitSessionReady(ctx, rt, name); err != nil {
		return err
	}
	respBody, status, err := f.agentReq(ctx, rt, http.MethodPost, inputPath, body)
	if err != nil {
		return err
	}
	if status >= 300 {
		return fmt.Errorf("send after resume %d: %s", status, strings.TrimSpace(string(respBody)))
	}
	return nil
}

// createReuseSession creates the long-lived session for reuse and returns the Agent's
// assigned session name (stored in the reuse ledger so later fires find it). title lets a
// human recognize it in the Console; the idempotency key collapses a CP-restart re-fire.
func (f *wakeFirer) createReuseSession(ctx context.Context, rt Runtime, sch Schedule, slot time.Time, title string) (string, error) {
	body := buildReuseCreateBody(sch, slot, title)
	respBody, status, err := f.agentReq(ctx, rt, http.MethodPost, "/sessions", body)
	if err != nil {
		return "", err
	}
	if status >= 300 {
		return "", fmt.Errorf("create_session %d: %s", status, strings.TrimSpace(string(respBody)))
	}
	var wire struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(respBody, &wire); err != nil || wire.Name == "" {
		return "", fmt.Errorf("create_session: could not read session name from response")
	}
	return wire.Name, nil
}

// retireSession best-effort archives the outgoing managed session on rotation so it stops
// occupying a slot and lands in the cleanup machinery. A failure is logged, not fatal —
// the new session is created regardless.
func (f *wakeFirer) retireSession(ctx context.Context, rt Runtime, name string) {
	if _, _, err := f.agentReq(ctx, rt, http.MethodPost, "/sessions/"+url.PathEscape(name)+"/archive", nil); err != nil {
		log.Printf("scheduler: retire session %s: %v", name, err)
	}
}

// awaitSessionReady polls the session's /status until it reports input-ready (alive AND
// its composer is drawn — the Agent's sessionInputReady), or a deadline passes. This is
// the delivery precondition the old awaitSessionAlive missed: liveness ("a tmux pane
// exists") is not readiness ("the CLI can accept a prompt"), so a reuse send gated only on
// alive could type into a booting/zombie pane and lose the prompt while /input returned 200
// and reuse_run_count still advanced. A timeout surfaces as an error, so the fire is
// recorded "error:" and the operator is notified — never a bogus "fired" with an unadvanced
// session.
func (f *wakeFirer) awaitSessionReady(ctx context.Context, rt Runtime, name string) error {
	timeout := f.readyTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	interval := f.readyInterval
	if interval <= 0 {
		interval = time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		if ready, err := f.sessionReady(ctx, rt, name); err == nil && ready {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for session %s to become input-ready", name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// sessionReady reads the Agent's /status input-readiness for one session (alive && ready).
// A transport/HTTP/parse error is returned so the caller's poll can retry within its
// deadline rather than treating a hiccup as "not ready forever".
func (f *wakeFirer) sessionReady(ctx context.Context, rt Runtime, name string) (bool, error) {
	body, status, err := f.agentReq(ctx, rt, http.MethodGet, "/sessions/"+url.PathEscape(name)+"/status", nil)
	if err != nil {
		return false, err
	}
	if status >= 300 {
		return false, fmt.Errorf("status %d", status)
	}
	var st struct {
		Alive bool `json:"alive"`
		Ready bool `json:"ready"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		return false, err
	}
	return st.Alive && st.Ready, nil
}

// saveReuse persists the reuse ledger, logging (not failing the fire) on a write error —
// a lost ledger update at worst recreates the session on the next fire.
func (f *wakeFirer) saveReuse(ctx context.Context, id, session, startedAt string, runCount int) {
	if startedAtT, err := time.Parse(time.RFC3339, startedAt); err == nil {
		startedAt = startedAtT.UTC().Format(time.RFC3339)
	}
	if err := f.mgr.store.SetScheduleReuse(ctx, id, session, startedAt, runCount, time.Now().UTC().Format(time.RFC3339)); err != nil {
		log.Printf("scheduler: save reuse ledger %s: %v", id, err)
	}
}

// agentReq performs one CP→Agent HTTP call and returns the response body + status. A
// transport error (err != nil) is distinct from an HTTP error status the caller
// interprets (e.g. 409 to trigger a resume).
func (f *wakeFirer) agentReq(ctx context.Context, rt Runtime, method, path string, body []byte) ([]byte, int, error) {
	return f.agentReqClient(ctx, agentHTTPClient, rt, method, path, body)
}

// agentReqClient is agentReq with an explicit client — the assistant-turn fire
// passes agentLongCallClient because its 8-minute ctx (not the shared 2-minute
// client timeout) is the intended bound.
func (f *wakeFirer) agentReqClient(ctx context.Context, cl *http.Client, rt Runtime, method, path string, body []byte) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rt.Endpoint()+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token())
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	return b, resp.StatusCode, nil
}

// --- pure helpers (unit-tested) -------------------------------------------------

// respHasCode reports whether an Agent JSON error body carries {"code": code}. Lets the
// reuse send distinguish a "stopped between check and POST" 409 (not_running -> resume and
// retry) from other conflicts (e.g. question_pending) that must surface as errors rather
// than trigger a spurious resend. Mirrors the Agent-side agentHTTPError.hasCode.
func respHasCode(body []byte, code string) bool {
	var b struct {
		Code string `json:"code"`
	}
	return json.Unmarshal(body, &b) == nil && b.Code == code
}

// findSessionByName returns the session with the given name, or nil. An empty name never
// matches (a reuse schedule with no adopted session yet).
func findSessionByName(sessions []sessionWire, name string) *sessionWire {
	if name == "" {
		return nil
	}
	for i := range sessions {
		if sessions[i].Name == name {
			return &sessions[i]
		}
	}
	return nil
}

// sessionBusy reports whether a session's state means a turn is in flight, so a reuse send
// would overlap it (subject to overlap_policy).
func sessionBusy(state string) bool { return state == "working" || state == "question" }

// reuseOverlap returns the schedule's overlap policy, defaulting to skip.
func reuseOverlap(sch Schedule) string {
	if sch.OverlapPolicy == "" {
		return "skip"
	}
	return sch.OverlapPolicy
}

// reuseMissingPolicy returns the pinned-target-missing policy, defaulting to recreate.
func reuseMissingPolicy(sch Schedule) string {
	if sch.MissingTargetPolicy == "" {
		return "recreate"
	}
	return sch.MissingTargetPolicy
}

// reuseCreateTitle is the human-readable title for a reuse session the scheduler creates.
// A pinned recreate adopts the operator's target name so the replacement is recognizable;
// a managed session is titled from the label/id.
func reuseCreateTitle(sch Schedule, pinned bool) string {
	if pinned {
		return sch.ReuseTarget
	}
	if sch.SpecLabel != "" {
		return "定時: " + sch.SpecLabel
	}
	return "定時: " + sch.ID
}

// reuseSendBody is the /input body for a reuse fire: the expanded prompt plus report_to
// (the operator conversation when report=true, else empty = no completion report).
// confirm (docs/log/38 配達検証) is the second sbk7oej fix: the first (the readiness gate)
// still declared "fired" on tmux keystroke success, and a CLI that momentarily could
// not accept input (cold resume before slash commands register, a swallowed Enter)
// silently ate the prompt while the ledger recorded success (2026-07-24 朝の再発).
// With confirm the Agent blocks until the prompt provably became a turn (a user line
// appended to the conversation log), self-heals once, and otherwise answers
// delivery_unconfirmed — which lands here as an error: status plus a notification,
// never a bogus "fired".
func reuseSendBody(sch Schedule, slot time.Time) []byte {
	b, _ := json.Marshal(map[string]any{
		"prompt":    expandSchedulePrompt(sch, slot),
		"report_to": scheduleReportTo(sch),
		"confirm":   true,
		"source":    scheduleSource(sch), // mirror badge: 定期/手動発火 (docs/log/38)
	})
	return b
}

// buildReuseCreateBody marshals the create_session body for a reuse session — the same
// fields as a new-mode fire plus a title so the created session is recognizable.
func buildReuseCreateBody(sch Schedule, slot time.Time, title string) []byte {
	kind := scheduleInjectKind(sch.AgentKind)
	body := map[string]any{
		"dir":             sch.Repo,
		"title":           title,
		"kind":            kind,
		"model":           sch.Model,
		"initial_prompt":  expandSchedulePrompt(sch, slot),
		"driver":          injectDriver(kind),
		"report_to":       scheduleReportTo(sch),
		"idempotency_key": scheduleIdempotencyKey(sch.ID, slot),
		"source":          scheduleSource(sch), // mirror badge: 定期/手動発火 (docs/log/38)
	}
	b, _ := json.Marshal(body)
	return b
}

// --- rotation ------------------------------------------------------------------

// rotationSpec is the parsed rotation policy (docs/log/38 P6). Triggers are OR-composed: any
// satisfied trigger rotates the managed session on the next fire. context_pct is reserved
// for a later best-effort usage trigger (running-only) and is ignored by rotationDue.
type rotationSpec struct {
	EveryRuns  int    `json:"every_runs"`
	After      string `json:"after"`
	Calendar   string `json:"calendar"`
	ContextPct int    `json:"context_pct"`
}

func (r rotationSpec) isEmpty() bool {
	return r.EveryRuns <= 0 && strings.TrimSpace(r.After) == "" && strings.TrimSpace(r.Calendar) == ""
}

// parseRotation parses the rotation JSON blob. An empty string is "no rotation".
func parseRotation(s string) (rotationSpec, error) {
	var r rotationSpec
	if strings.TrimSpace(s) == "" {
		return r, nil
	}
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		return rotationSpec{}, fmt.Errorf("rotation JSON: %w", err)
	}
	return r, nil
}

// validateRotation checks the rotation JSON at create/update (operator API). An empty
// string is valid (no rotation). every_runs must be non-negative, after must parse as a
// duration, and calendar must be one of the known granularities.
func validateRotation(s string) error {
	r, err := parseRotation(s)
	if err != nil {
		return err
	}
	if r.EveryRuns < 0 {
		return fmt.Errorf("rotation every_runs must be >= 0")
	}
	if strings.TrimSpace(r.After) != "" {
		if _, ok := parseRotateDuration(r.After); !ok {
			return fmt.Errorf("rotation after %q: want a duration like 7d, 12h, or 30m", r.After)
		}
	}
	if r.Calendar != "" && !oneOf(r.Calendar, "daily", "weekly", "monthly") {
		return fmt.Errorf("rotation calendar must be daily, weekly, or monthly")
	}
	return nil
}

// rotationDue reports whether the current managed reuse session should be retired before
// this fire. Deterministic triggers only (v1): every_runs (fires since last rotation),
// after (age of the current session), calendar (a boundary crossed). loc is the
// schedule's zone so calendar boundaries align to the user's week/day.
func rotationDue(sch Schedule, slot time.Time, loc *time.Location) bool {
	r, err := parseRotation(sch.Rotation)
	if err != nil || r.isEmpty() {
		return false
	}
	if r.EveryRuns > 0 && sch.ReuseRunCount >= r.EveryRuns {
		return true
	}
	started, perr := time.Parse(time.RFC3339, sch.ReuseStartedAt)
	if perr != nil {
		return false // no baseline yet — period/calendar cannot trigger
	}
	if d, ok := parseRotateDuration(r.After); ok && d > 0 && !slot.Before(started.Add(d)) {
		return true
	}
	if r.Calendar != "" && calendarCrossed(started.In(loc), slot.In(loc), r.Calendar) {
		return true
	}
	return false
}

// parseRotateDuration accepts a Go duration ("12h", "30m") or a whole-day form ("7d").
func parseRotateDuration(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if strings.HasSuffix(s, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour, true
		}
		return 0, false
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d, true
	}
	return 0, false
}

// calendarCrossed reports whether slot falls in a later calendar bucket than started, for
// the given granularity. "weekly" uses ISO year-week, so "月曜は新セッション" (a Monday
// boundary) rotates whenever a fire lands in a new week.
func calendarCrossed(started, slot time.Time, calendar string) bool {
	switch calendar {
	case "daily":
		sy, sm, sd := started.Date()
		ty, tm, td := slot.Date()
		return sy != ty || sm != tm || sd != td
	case "weekly":
		sy, sw := started.ISOWeek()
		ty, tw := slot.ISOWeek()
		return sy != ty || sw != tw
	case "monthly":
		sy, sm, _ := started.Date()
		ty, tm, _ := slot.Date()
		return sy != ty || sm != tm
	default:
		return false
	}
}
