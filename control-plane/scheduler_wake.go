package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Scheduled execution P2 (docs/log/38 + ADR0021): the real scheduleFirer that turns a
// due schedule into a running session. It fills the seam P1 left behind logFirer.
//
// Flow per fire:
//  1. resolve the schedule's owner membership -> *resolved (runtime + records),
//     reusing the header-less MCP path (IdentityIDForMembership + resolveByMembership,
//     the same one the memo bridge flush uses).
//  2. apply the wake policy against the live workspace state (wake / skip / catch_up).
//  3. register a reaper keep-alive so scale-to-zero cannot stop the workspace out from
//     under the just-woken session (★1), released after a settle window.
//  4. wait for the Agent to become reachable, then inject a session via the Agent's
//     create_session REST (POST /sessions) with report_to + a deterministic idempotency
//     key. Completion rides docs/log/30 back to the operator conversation — no new report path.
//
// Deferred to later phases (kept explicit so the seam is honest): jitter / wake
// concurrency caps / rate-limit pre-checks / unattended-failure reporting are P4; the
// repo/worktree/new_branch -> Agent create fields and prompt-var whitelist validation
// are finalized alongside the operator MCP in P3. P2 injects the execution nucleus
// (kind/model/expanded-prompt/report_to/idempotency) and passes dir through verbatim.

type wakeFirer struct {
	mgr   *manager
	wsAPI workspaceAPI // for ensureWorkspaceStarted (quota + docs staging + Start)
	// settle is how long the keep-alive is held after a fire so the reaper does not
	// reclaim the freshly-woken workspace before its session runs and reports (★1).
	settle time.Duration
	// readyTimeout bounds the wait for the Agent to come up after a wake.
	readyTimeout time.Duration
	// sessionReadyTimeout bounds the per-session input-readiness wait a reuse send makes
	// before typing (awaitSessionReady). Deliberately SEPARATE from readyTimeout: the two
	// wait for different things. readyTimeout covers a container boot (minutes on ECS),
	// while this one covers a CLI drawing its composer in an already-running container —
	// tying them together would make a wedged session hold a fire slot for the whole boot
	// budget, on a workspace that is demonstrably up.
	sessionReadyTimeout time.Duration
	// readyInterval is the poll gap for awaitSessionReady (0 -> 1s). Tests set it small.
	readyInterval time.Duration
}

// scheduleSessionReadyWait is the default sessionReadyTimeout: how long a reuse fire waits
// for its target session to become input-ready once the Agent is answering.
const scheduleSessionReadyWait = 90 * time.Second

func newWakeFirer(mgr *manager, settle, readyTimeout time.Duration) *wakeFirer {
	return &wakeFirer{
		mgr:                 mgr,
		wsAPI:               newWorkspaceAPI(mgr, true),
		settle:              settle,
		readyTimeout:        readyTimeout,
		sessionReadyTimeout: scheduleSessionReadyWait,
	}
}

func (f *wakeFirer) fire(ctx context.Context, sch store.Schedule, slot time.Time) (string, string, error) {
	// 1. membership -> resolved (no gateway headers; the MCP/memo-bridge path).
	identityID, ok, err := f.mgr.store.IdentityIDForMembership(ctx, sch.MembershipID)
	if err != nil {
		return "", "", fmt.Errorf("resolve identity: %w", err)
	}
	if !ok {
		// Soft outcome: the membership was revoked. Record it (not an error) so the
		// ledger advances and the operator can see why nothing ran.
		return statusMembershipInactive, "", nil
	}
	res, aerr := f.mgr.resolveByMembership(ctx, identityID, sch.MembershipID)
	if aerr != nil {
		if aerr.code == "forbidden_tenant" {
			return statusMembershipInactive, "", nil
		}
		return "", "", fmt.Errorf("resolve membership: %s", aerr.message)
	}

	// 2. wake policy vs live state.
	state := res.rt.State(ctx)
	shouldWake, soft := wakeDecision(state, sch.WakePolicy)
	if soft != "" {
		return soft, "", nil
	}
	// 3. keep-alive: hold a tier-2 pseudo-connection so the reaper cannot stop this
	//    workspace while the session runs; release after the settle window (★1). The
	//    settle grace also covers auto-turn follow-ups and a user opening the Console
	//    right after. A workspace that was already running before this fire is left to
	//    the reaper's normal timeout (we still hold+release, which only defers it).
	//
	//    Armed BEFORE the wake, not after: a start that overruns its health budget used
	//    to return early from here, leaving a container that was still coming up with no
	//    keep-alive at all — the reaper could then reclaim the very workspace this fire
	//    had just booted. Holding it across the start closes that window, and the
	//    AfterFunc release runs on every path (including the early returns below).
	releasePresence, presenceErr := f.mgr.trackWorkspaceConnection(ctx, res.ws.ID, "")
	if presenceErr != nil {
		if errors.Is(presenceErr, errWorkspaceStopping) {
			return "skipped_stopped", "", nil
		}
		return "", "", presenceErr
	}
	f.scheduleRelease(releasePresence)

	var wakeErr error
	if shouldWake {
		if aerr := f.wsAPI.ensureWorkspaceStartedUnattended(ctx, res); aerr != nil {
			if aerr.code == "quota_workspaces" {
				return "skipped_quota", "", nil // soft: tenant is at its running cap
			}
			// NOT fatal on its own. A readiness overrun is no longer reported as a start
			// failure at all (runtime_health.go), so this now only catches real ones —
			// but the tolerance stays: the very next step polls the Agent patiently for
			// readyTimeout, and a start error that the Agent then contradicts by coming
			// up must not lose the fire. Remember the error and let awaitAgentReady
			// adjudicate: if the Agent does come up, the fire proceeds; if it does not,
			// the failure below reports both causes.
			wakeErr = fmt.Errorf("wake: %s", aerr.message)
		}
	}

	// 4. wait for the Agent, then inject.
	//
	// A failure HERE is retryable and is marked as such: nothing has been delivered yet,
	// so re-running this same slot cannot double-deliver, and on a substrate whose boot
	// time is measured in minutes (ecs-ec2) the workspace this fire just woke is usually
	// answering a tick or two later. Past this point the fire has started talking to the
	// Agent and a retry could duplicate a prompt, so nothing below is retryable.
	if err := f.awaitAgentReady(ctx, res.rt); err != nil {
		if wakeErr != nil {
			return "", "", retryableFireErr(fmt.Errorf("%w (after %v)", err, wakeErr))
		}
		return "", "", retryableFireErr(fmt.Errorf("agent not ready: %w", err))
	}
	if wakeErr != nil {
		// The start reported failure but the Agent is up — it was only slow. Finish the
		// bookkeeping ensureWorkspaceStarted skipped on its error path, so the workspace
		// is not left recorded as stopped while it is serving this fire.
		log.Printf("scheduler: %s recovered — agent became ready after the start deadline (schedule %s)", res.ws.ID, sch.ID)
		_ = f.mgr.store.SetWorkspaceState(ctx, res.ws.ID, "running")
		_ = f.mgr.touchWorkspace(ctx, res.ws.ID)
	}
	// session_mode=assistant: run one assistant-chat turn
	// instead of driving a session.
	if sch.SessionMode == "assistant" {
		return f.fireAssistant(ctx, res, sch, slot)
	}
	// session_mode=reuse (P6): send into the long-lived session instead of creating one.
	if sch.SessionMode == "reuse" {
		return f.fireReuse(ctx, res, sch, slot)
	}
	session, err := f.injectSession(ctx, res.rt, buildInjectBody(sch, slot))
	if err != nil {
		return "", "", fmt.Errorf("inject: %w", err)
	}
	return "fired", session, nil
}

// scheduleRelease drops the keep-alive after the settle window. time.AfterFunc runs on
// its own goroutine so the fire path does not block; doneConn is safe on an already-
// gone workspace (it only decrements a positive counter).
func (f *wakeFirer) scheduleRelease(release func()) {
	settle := f.settle
	if settle <= 0 {
		settle = 5 * time.Minute
	}
	time.AfterFunc(settle, release)
}

// awaitAgentReady polls the Agent's session list until it responds or readyTimeout
// elapses — a just-started container's Agent needs a moment before it accepts a create.
//
// ⚠️ The clock starts HERE, at the wake, not at the runtime's own notion of "Start".
// On ecs-ec2 a stopped slot's StartInstances / ECS registration / home mount all run in
// the adapter's BACKGROUND convergence (place.deferred) and Start returns immediately, so
// roughly 20 seconds of the boot are already spent before the adapter's "Agent healthy Ns
// after Start" clock even begins. A budget picked against that number is ~20s tighter than
// it reads (measured on the acrt deployment: a fire that logged "healthy 73s after Start"
// was 93.8s after the wake, and was dropped by the old 90s budget).
func (f *wakeFirer) awaitAgentReady(ctx context.Context, rt runtime.Runtime) error {
	deadline := time.Now().Add(f.readyTimeout)
	if f.readyTimeout <= 0 {
		deadline = time.Now().Add(runtime.AgentBootBudget)
	}
	var lastErr error
	for {
		if _, err := f.mgr.agentSessions(ctx, rt); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for agent: %w", lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// injectSession POSTs a create_session to the Agent (same REST the Console/MCP use) and
// returns the created session's name so the run history can link to it. The idempotency
// key collapses a retry onto the first session, and the Agent replays that session's wire
// body verbatim, so the returned name is stable across a CP restart that re-fires the slot.
func (f *wakeFirer) injectSession(ctx context.Context, rt runtime.Runtime, body []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", rt.Endpoint()+"/sessions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token())
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("agent create_session %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	// The response is the wire session; pull its name (best-effort — a fire whose session
	// name we cannot parse still counts as fired, it just has no history link).
	var created struct {
		Name string `json:"name"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	_ = json.Unmarshal(b, &created)
	return created.Name, nil
}

// --- pure helpers (unit-tested) -------------------------------------------------

// wakeDecision maps the live workspace state + policy to (shouldWake, softStatus).
// A non-empty softStatus means "do not run, record this status and advance" — used
// when the policy declines to wake a stopped workspace. A running workspace always
// proceeds (shouldWake=false, soft=""), regardless of policy.
func wakeDecision(state, policy string) (shouldWake bool, softStatus string) {
	if state == "running" {
		return false, ""
	}
	// starting: a launch is already converging; treat like a wake in progress and let
	// the readiness wait catch up rather than double-driving Start.
	switch policy {
	case "skip":
		return false, "skipped_stopped"
	default: // "wake" (default) and "catch_up" both bring it up. The catch_up vs skip
		// distinction on missed slots is moot here because the ledger already collapses
		// a backlog to a single fire (advanceNextRun uses now).
		return true, ""
	}
}

// scheduleInjectKind defaults a blank agent kind to claude.
func scheduleInjectKind(kind string) string {
	if strings.TrimSpace(kind) == "" {
		return "claude"
	}
	return kind
}

// injectDriver mirrors the Agent-side rule: managed-driver kinds run paneless.
func injectDriver(kind string) string {
	switch kind {
	case "codex", "opencode", "copilot", "cursor", "kiro":
		return "managed"
	default:
		return ""
	}
}

// scheduleSource is the injection-origin tag stamped on every prompt this scheduler
// delivers (create initial_prompt and reuse /input), so the mirror can badge the turn
// as schedule-driven — and tell a run-now (manual fire) from a timed one. The
// Agent whitelists these values (session_injections.go injectionSource).
func scheduleSource(sch store.Schedule) string {
	if sch.ManualFirePending {
		return "schedule-manual"
	}
	return "schedule"
}

// scheduleReportTo is the docs/log/30 report_to target for a fire: the owner conversation
// when the schedule opted into completion reports (report=true), empty otherwise — an
// empty report_to disables the report obligation on the Agent side, so the default is
// a silent fire (the run history / failure notifications still surface it).
func scheduleReportTo(sch store.Schedule) string {
	if sch.Report {
		return sch.OwnerConv
	}
	return ""
}

// scheduleIdempotencyKey derives a deterministic create key from (schedule, slot) so a
// CP restart that re-fires the same slot collapses onto the first session via the
// Agent's create_session ledger (★4). The slot (not now) is the dedupe axis.
func scheduleIdempotencyKey(scheduleID string, slot time.Time) string {
	return "sch_" + scheduleID + "@" + slot.UTC().Format(time.RFC3339)
}

// buildInjectBody marshals the Agent create_session request for a scheduled fire.
func buildInjectBody(sch store.Schedule, slot time.Time) []byte {
	kind := scheduleInjectKind(sch.AgentKind)
	body := map[string]any{
		"dir":             sch.Repo, // P2 verbatim passthrough; repo/worktree resolution is P3
		"kind":            kind,
		"model":           sch.Model,
		"initial_prompt":  expandSchedulePrompt(sch, slot),
		"driver":          injectDriver(kind),
		"report_to":       scheduleReportTo(sch),
		"idempotency_key": scheduleIdempotencyKey(sch.ID, slot),
		"source":          scheduleSource(sch), // mirror badge: scheduled vs manual fire
	}
	b, _ := json.Marshal(body)
	return b
}

// expandSchedulePrompt substitutes the fixed metadata template variables (ADR0021 §④”')
// into the prompt. Every value is deterministically computed by the scheduler from the
// fire slot and the schedule row — no user/report/external data is ever injected, so the
// prompt-injection surface stays zero. An undefined {{foo}} is passed through literally.
func expandSchedulePrompt(sch store.Schedule, slot time.Time) string {
	loc := scheduleLocation(sch.TZ)
	st := slot.In(loc)
	lastRun := ""
	if t, err := time.Parse(time.RFC3339, sch.LastRun); err == nil {
		lastRun = t.In(loc).Format("2006-01-02 15:04")
	}
	tz := sch.TZ
	if tz == "" {
		tz = "UTC"
	}
	repl := strings.NewReplacer(
		"{{date}}", st.Format("2006-01-02"),
		"{{time}}", st.Format("15:04"),
		"{{datetime}}", st.Format("2006-01-02 15:04 MST"),
		"{{tz}}", tz,
		"{{schedule_id}}", sch.ID,
		"{{schedule_label}}", sch.SpecLabel,
		"{{last_run}}", lastRun,
	)
	return repl.Replace(sch.Prompt)
}

// logSchedulerFirerNote records once, at wiring time, which firer is active.
func logSchedulerFirerNote(f scheduleFirer) {
	if _, ok := f.(*wakeFirer); ok {
		log.Printf("scheduler: wake firer active (P2 — will wake + inject on due)")
	}
}
