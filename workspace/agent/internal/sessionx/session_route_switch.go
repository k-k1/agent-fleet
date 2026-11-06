package sessionx

// Switching a session away from a stopped subscription (ADR 0128 decision 10, B1).
//
// A session on a subscription route that stopped on a limit which waiting clears may be moved to
// the metered route its harness declares (claude: anthropic-subscription -> anthropic-api), on a
// credential the member holds. The move is an exclusive state transition. In order, each step
// persisted in the switch record before the next one starts:
//
//	begun    the checks passed under the session's exclusive lifecycle gate; nothing has changed
//	booking  the usage-limit resume booking is recorded as cancelled and deleted at the CP, so a
//	         stale "carry on" can never land on the metered route
//	stopped  the old runtime is stopped; the boundary instant is fixed
//	stored   the new selection and the boundary are on the meta, in one write
//	resumed  the session runs on the new route
//
// The gate is taken exclusively for the whole transition (lifegate.Lock): every input path holds
// it shared, so a member message, a peer message or a scheduled prompt waits at the gate and
// lands after the switch, on the route the session has then. Nothing is admitted in between, and
// none that was admitted before the lock is still running when the checks are made.
//
// Any failure rolls back: the selection and boundary are taken off the meta, a pane the new
// route started is stopped, the booking is made again, and, if the session was running, the old
// runtime is started again. The invariant at every outcome is that the stored selection is the
// route a start of the session would use; the new route is never stored beside the old runtime.
// A crash is recovered from the record (RecoverRouteSwitches): what the meta and the process say
// decides, because the record can lag a write by one step.
//
// Not decided here: B2's "resume on usage credits already enabled". Nothing the Agent reads says
// whether credits are enabled on the login (the transcript shows the window error, never the
// account setting), so the preview carries only the account-setting link. The Agent never
// toggles credits.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fleetgraph"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/lifegate"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The steps of a switch, in the order they are reached.
const (
	rsBegun    = "begun"
	rsBooking  = "booking"
	rsStopped  = "stopped"
	rsStored   = "stored"
	rsResumed  = "resumed"
	rsRollback = "rolling-back"
)

// routeSwitchRecord is the persisted state of one switch, from begun until it is finished or
// rolled back. It outlives a crash and holds what a rollback needs.
type routeSwitchRecord struct {
	Session string            `json:"session"`
	From    *routes.Selection `json:"from,omitempty"`
	To      routes.Selection  `json:"to"`
	Step    string            `json:"step"`
	// At is the boundary instant (Meta.RouteSwitch.At); empty until the old runtime is stopped.
	At       string `json:"at,omitempty"`
	WasAlive bool   `json:"wasAlive,omitempty"`
	// Booking is the usage-limit resume booking this switch cancels, written before the cancel.
	Booking   string `json:"booking,omitempty"`
	StartedAt string `json:"startedAt"`
	Error     string `json:"error,omitempty"`
}

var routeSwitchRecords = fstore.JSON[routeSwitchRecord](paths.AgentStateDir, "session-route-switch", ".json")

// accountUsageSettingURL is where usage credits are enabled on the Claude account (B2's link).
// The setting belongs to the account: it affects every session and the assistant chat on the
// login, which the Console says next to the link. Not measured against a live account.
const accountUsageSettingURL = "https://claude.ai/settings/usage"

// Side effects behind seams: tests have neither tmux nor a transcript.
var (
	routeSwitchAtLimit = func(m session.Meta) (agents.LimitKind, bool) { return atUsageLimit(m) }
	routeSwitchHalt    = haltSessionMeta
	routeSwitchLaunch  = startSessionTmux
	routeSwitchAlive   = SessionAlive
	// routeSwitchAfterStep runs after a step is persisted. A test returns errRouteSwitchCrash to
	// stop the switch dead at that boundary, as a process that died there would.
	routeSwitchAfterStep func(step string) error
)

func init() {
	// A switch record that outlives its run (an incomplete rollback, or a crash before boot
	// recovery has settled it) keeps the session's input closed: the stored selection and the
	// runtime may disagree until it is settled, and a turn run in between would be filed under
	// the wrong route. The running switch holds the gate exclusively and never enters it.
	lifegate.SetHeldCheck(func(name string) string {
		if _, ok := routeSwitchRecords.ModTime(name); ok {
			return "an unfinished route switch is being settled"
		}
		return ""
	})
}

var errRouteSwitchCrash = errors.New("route switch: simulated crash")

// routeSwitchError is a refusal or failure of the switch as the API answers it.
type routeSwitchError struct {
	Status  int
	Code    string
	Message string
	// Session is the session as it stands after a failure that touched it.
	Session *session.Session
}

func (e *routeSwitchError) Error() string { return e.Message }

func refusal(status int, code, msg string) *routeSwitchError {
	return &routeSwitchError{Status: status, Code: code, Message: msg}
}

func fromRefusal(r *agents.RouteRefusal) *routeSwitchError {
	return &routeSwitchError{Status: r.Status, Code: r.Code, Message: r.Message}
}

// --- the checks ----------------------------------------------------------------------------

// checkRouteSwitch is everything that decides whether m may be switched, short of changing
// anything. It returns the selection to store (bound to the connection it was checked on). The
// preview runs it without the gate, as an answer that can be stale by the time the member
// confirms; the switch runs it again under the exclusive gate.
//
// An empty selection is handled by the harness (RouteSwitcher.SwitchTarget): it is allowed only
// when the harness can show the session runs on the subscription, and refused as unproven
// otherwise (ADR 0128 decisions 2 and 6).
func checkRouteSwitch(ctx context.Context, m session.Meta, want string) (routes.Selection, *routeSwitchError) {
	a, _ := RegisteredAgent(NormalizeKind(m.Kind))
	sw, ok := a.(agents.RouteSwitcher)
	if !ok {
		return routes.Selection{}, refusal(http.StatusBadRequest, routes.CodeSwitchUnsupported,
			"this kind has no route to switch to")
	}
	if m.MovedTo != "" || m.Moving != "" {
		return routes.Selection{}, refusal(http.StatusConflict, routes.CodeSwitchBusy, "the session is being moved or was moved")
	}
	if m.DriverKind() != session.DriverTUI {
		return routes.Selection{}, refusal(http.StatusBadRequest, routes.CodeSwitchUnsupported,
			"a route switch is for a Terminal (CLI) session")
	}
	target, ref := sw.SwitchTarget(m)
	if ref != nil {
		return routes.Selection{}, fromRefusal(ref)
	}
	if want != "" && want != target.Route {
		return routes.Selection{}, refusal(http.StatusBadRequest, routes.CodeSwitchUnsupported,
			"this session can be switched to "+target.Route+" only, not "+want)
	}
	// Limit evidence on the route the session is on now, of the kind waiting clears. A spend
	// limit is a billing decision, and a metered route is not an answer to it.
	kind, atLimit := routeSwitchAtLimit(m)
	switch {
	case !atLimit:
		return routes.Selection{}, refusal(http.StatusConflict, routes.CodeSwitchNoLimit,
			"the session is not stopped on a usage limit of its current route")
	case kind == agents.LimitSpend:
		return routes.Selection{}, refusal(http.StatusConflict, routes.CodeSwitchSpendLimit,
			"the session stopped on a spend limit; waiting does not clear it, and a switch of route is not the way past it")
	}
	state, blocker := moveSourceState(m)
	if blocker != "" && blocker != agents.StateAuth { // an expired subscription login is no reason not to leave it
		return routes.Selection{}, refusal(http.StatusConflict, routes.CodeSwitchState, "session "+m.Name+" is waiting on "+blocker)
	}
	if !movableSourceState(state) {
		return routes.Selection{}, refusal(http.StatusConflict, routes.CodeSwitchState,
			fmt.Sprintf("session %s is %s; a switch needs it idle, stopped or at its usage limit", m.Name, state))
	}
	// Decision 12, Agent side, before anything is stopped: permission is use, the connection is
	// available, the model is valid on the new route, and a start on it would be accepted.
	if ref := checkRouteLive(ctx, m.Kind, &target, m.Model); ref != nil {
		return routes.Selection{}, fromRefusal(ref)
	}
	if b, ok := a.(agents.RouteBinder); ok {
		bound, ref := b.BindRoute(target)
		if ref != nil {
			return routes.Selection{}, fromRefusal(ref)
		}
		target = bound
	}
	cand := m
	cand.Route = target.Clone()
	if ref := startRefusalOf(cand); ref != nil {
		return routes.Selection{}, refusal(ref.Status, ref.Code, ref.Message)
	}
	return target, nil
}

// --- the preview ---------------------------------------------------------------------------

type routeSwitchPreview struct {
	// Allowed: a switch would be accepted now. The Console offers it only then.
	Allowed bool   `json:"allowed"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	// Code and Reason say why not, from routes.CodeSwitch*.
	Code   string `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
	// MayCostMore and CacheNotShared are the two caveats the offer states (decision 10): the
	// metered route bills per token, and the prompt cache is not guaranteed to carry across.
	MayCostMore    bool `json:"mayCostMore,omitempty"`
	CacheNotShared bool `json:"cacheNotShared,omitempty"`
	// AccountSettingURL is B2's link to the account's usage-credit setting, with the statement
	// the Console must put beside it: it affects every session on the login, and the Agent
	// never changes it. Credits already being enabled is not detectable from anything the Agent
	// reads, so there is no "resume on credits" action.
	AccountSettingURL        string `json:"accountSettingUrl,omitempty"`
	AccountSettingAffectsAll bool   `json:"accountSettingAffectsAll,omitempty"`
	// Pending: an earlier switch of this session did not finish.
	Pending bool `json:"pending,omitempty"`
}

// HandleSessionRouteSwitchPreview answers GET /sessions/{name}/route-switch.
func HandleSessionRouteSwitchPreview(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, routeSwitchPreviewOf(r.Context(), m))
}

func routeSwitchPreviewOf(ctx context.Context, m session.Meta) routeSwitchPreview {
	p := routeSwitchPreview{}
	if m.Route != nil {
		p.From = m.Route.Route
	}
	if _, pending := routeSwitchRecords.Read(m.Name); pending {
		p.Pending = true
		p.Code, p.Reason = routes.CodeSwitchBusy, "an earlier switch of this session did not finish"
		return p
	}
	to, rerr := checkRouteSwitch(ctx, m, "")
	if rerr != nil {
		p.Code, p.Reason = rerr.Code, rerr.Message
		return p
	}
	p.Allowed, p.To = true, to.Route
	p.MayCostMore, p.CacheNotShared = true, true
	p.AccountSettingURL, p.AccountSettingAffectsAll = accountUsageSettingURL, true
	return p
}

// --- the switch ----------------------------------------------------------------------------

type routeSwitchRequest struct {
	// Route, when given, must be the route the harness declares; omitted means "that one".
	Route string `json:"route"`
}

type routeSwitchWire struct {
	Session session.Session `json:"session"`
	From    string          `json:"from,omitempty"`
	To      string          `json:"to"`
	// BookingCancelPending: the usage-limit resume was recorded as cancelled but the Control
	// Plane did not confirm the delete; the sweep retries, and a fire that arrives first is
	// refused.
	BookingCancelPending bool `json:"bookingCancelPending,omitempty"`
}

type routeSwitchFailureWire struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	// Session is the session as it stands after a failure that touched it.
	Session *session.Session `json:"session,omitempty"`
}

// HandleSessionRouteSwitch answers POST /sessions/{name}/route-switch {route?}.
func HandleSessionRouteSwitch(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var req routeSwitchRequest
	if r.ContentLength != 0 && !httpx.DecodeJSON(w, r, &req) {
		return
	}
	res, err := runRouteSwitch(r.Context(), name, strings.TrimSpace(req.Route))
	if err != nil {
		var se *routeSwitchError
		if !errors.As(err, &se) {
			se = refusal(http.StatusInternalServerError, routes.CodeSwitchFailed, err.Error())
		}
		var out routeSwitchFailureWire
		out.Error.Code, out.Error.Message, out.Session = se.Code, se.Message, se.Session
		httpx.WriteJSON(w, se.Status, out)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// routeSwitchRun is one switch in progress.
type routeSwitchRun struct {
	name string
	rec  routeSwitchRecord
	// orig is the session as it was before the switch.
	orig session.Meta
}

// runRouteSwitch is the switch, from the checks to the commit or the rollback.
func runRouteSwitch(ctx context.Context, name, want string) (*routeSwitchWire, error) {
	// One operation per session at a time with the move's own: a move and a switch both rewrite
	// the session's runtime and must not interleave.
	unlockOp, ok := moveOpLock(name)
	if !ok {
		return nil, refusal(http.StatusConflict, routes.CodeSwitchBusy, "a move or a switch of this session is already running")
	}
	defer unlockOp()
	if _, pending := routeSwitchRecords.Read(name); pending {
		return nil, refusal(http.StatusConflict, routes.CodeSwitchBusy, "an earlier switch of this session did not finish")
	}
	// Exclusive: hold pending input. Every input path holds the gate shared, so they wait here
	// until the switch is over and then act on the session as it is.
	x, err := lifegate.Lock(ctx, name)
	if err != nil {
		return nil, refusal(http.StatusServiceUnavailable, "session_gate", err.Error())
	}
	defer x.Unlock()
	m, ok := session.ReadMeta(name)
	if !ok {
		return nil, refusal(http.StatusNotFound, "not_found", "no such session: "+name)
	}
	to, rerr := checkRouteSwitch(ctx, m, want)
	if rerr != nil {
		return nil, rerr
	}
	// Nothing below is cut short by the caller going away: a half-done switch is what the
	// rollback exists for, not something to add to.
	ctx = context.WithoutCancel(ctx)
	run := &routeSwitchRun{name: name, orig: m, rec: routeSwitchRecord{
		Session: name, From: m.Route.Clone(), To: to, WasAlive: routeSwitchAlive(m),
		StartedAt: time.Now().Format(time.RFC3339),
	}}
	log.Printf("route-switch: %s: %s -> %s", name, routeNameOf(m.Route), to.Route)
	if err := run.persist(rsBegun); err != nil {
		if errors.Is(err, errRouteSwitchCrash) {
			return nil, err
		}
		routeSwitchRecords.Remove(name)
		return nil, refusal(http.StatusInternalServerError, routes.CodeSwitchFailed, "recording the switch: "+err.Error())
	}
	if err := run.forward(); err != nil {
		if errors.Is(err, errRouteSwitchCrash) {
			return nil, err
		}
		return nil, run.fail(err)
	}
	cur, _ := session.ReadMeta(name)
	res := &routeSwitchWire{Session: wireSession(cur, routeSwitchAlive(cur)), From: routeNameOf(m.Route), To: to.Route}
	if run.rec.Booking != "" {
		_, res.BookingCancelPending = pendingRateLimitCleanups()[run.rec.Booking]
	}
	log.Printf("route-switch: %s: now on %s", name, to.Route)
	return res, nil
}

func routeNameOf(s *routes.Selection) string {
	if s == nil {
		return ""
	}
	return s.Route
}

// persist records the step reached, then lets a test stop the switch there.
func (r *routeSwitchRun) persist(step string) error {
	r.rec.Step = step
	if err := routeSwitchRecords.Write(r.name, r.rec); err != nil {
		return err
	}
	if routeSwitchAfterStep != nil {
		return routeSwitchAfterStep(step)
	}
	return nil
}

// forward runs the steps after the record is written.
func (r *routeSwitchRun) forward() error {
	if err := r.cancelBooking(); err != nil {
		return err
	}
	if err := r.stopOld(); err != nil {
		return err
	}
	if err := r.store(); err != nil {
		return err
	}
	if err := r.resume(); err != nil {
		return err
	}
	return r.finish()
}

// cancelBooking records the cancellation of the session's resume booking and deletes it at the
// CP. A booking that cannot be recorded as cancelled fails the switch before anything changed
// (as a move's commit does); a delete the CP did not confirm stays recorded for the sweep, and a
// fire that arrives first is refused (refuseCancelledResume).
func (r *routeSwitchRun) cancelBooking() error {
	episodeMu := rateLimitEpisodeLock(r.name)
	episodeMu.Lock()
	st, has := RateLimitStates.Read(r.name)
	episodeMu.Unlock()
	if has && st.ScheduleID != "" {
		// The id goes into the switch record first: whatever happens after, a rollback knows which
		// booking to make again.
		r.rec.Booking = st.ScheduleID
	}
	if err := r.persist(rsBooking); err != nil {
		return err
	}
	if r.rec.Booking == "" {
		return nil
	}
	if err := recordRateLimitCancelKeep(r.name, r.rec.Booking, routeSwitchCancelKeep); err != nil {
		return &routeSwitchError{Status: http.StatusInternalServerError, Code: routes.CodeSwitchBooking,
			Message: "the usage-limit resume could not be recorded as cancelled, so nothing was changed: " + err.Error()}
	}
	finishRateLimitCancel(r.rec.Booking)
	return nil
}

// routeSwitchCancelKeep is how long a cancelled booking stays refusable after its episode let
// it go: a rolled-back switch rebooks under a new id, and a fire of the old one may still be
// waiting on the gate. Far longer than any in-flight request.
const routeSwitchCancelKeep = 24 * time.Hour

// stopOld stops the old runtime and fixes the boundary instant.
func (r *routeSwitchRun) stopOld() error {
	if r.rec.WasAlive {
		cur, ok := session.ReadMeta(r.name)
		if !ok {
			return errMoveSourceGone
		}
		if _, err := routeSwitchHalt(cur); err != nil {
			return fmt.Errorf("stopping the session: %w", err)
		}
	}
	r.rec.At = time.Now().UTC().Format(time.RFC3339Nano)
	return r.persist(rsStopped)
}

// store writes the new selection and the boundary in one write.
func (r *routeSwitchRun) store() error {
	_, ok, err := updateSessionMetaChecked(r.name, func(cur *session.Meta) bool {
		if cur.Moving != "" || cur.MovedTo != "" {
			return false
		}
		cur.Route = r.rec.To.Clone()
		cur.RouteSwitches = append(cur.RouteSwitches, session.RouteSwitch{At: r.rec.At, From: r.rec.From.Clone(), To: r.rec.To})
		return true
	})
	// Whatever was written, the old selection's tokens end here: the halt above already ended
	// them when the session was running, and a stopped session may still hold one.
	endRouteTokens(r.name)
	if err != nil {
		return fmt.Errorf("storing the new route: %w", err)
	}
	if !ok {
		return errors.New("the session changed or is gone; the new route was not stored")
	}
	return r.persist(rsStored)
}

// resume starts the session on the new route, after the start checks run once more on what is
// now stored.
func (r *routeSwitchRun) resume() error {
	cur, ok := session.ReadMeta(r.name)
	if !ok {
		return errMoveSourceGone
	}
	if ref := startRefusalOf(cur); ref != nil {
		return errors.New(ref.Message)
	}
	recallSettings(&cur)
	if err := routeSwitchLaunch(cur, false); err != nil {
		return fmt.Errorf("starting on %s: %w", r.rec.To.Route, err)
	}
	if _, cleared := UpdateSessionMeta(r.name, func(c *session.Meta) bool {
		if c.StoppedAt == "" {
			return false
		}
		c.StoppedAt = ""
		return true
	}); cleared {
		fleetgraph.RecordRevive(r.name)
	}
	return r.persist(rsResumed)
}

// finish is what follows a switch that is on its new route: the usage-limit episode of the old
// route closes (nothing shows the session as waiting for a reset), the booking's cancellation is
// settled once more, and the record goes.
func (r *routeSwitchRun) finish() error {
	finishRouteSwitch(r.name, r.rec)
	return nil
}

func finishRouteSwitch(name string, rec routeSwitchRecord) {
	episodeMu := rateLimitEpisodeLock(name)
	episodeMu.Lock()
	if _, has := RateLimitStates.Read(name); has {
		RateLimitStates.Remove(name)
	}
	episodeMu.Unlock()
	if rec.Booking != "" {
		finishRateLimitCancel(rec.Booking) // outside the lock: the CP may take its timeout
	}
	routeSwitchRecords.Remove(name)
}

// --- rollback ------------------------------------------------------------------------------

// fail rolls the switch back after cause and returns the answer that says what state the
// session is in.
func (r *routeSwitchRun) fail(cause error) error {
	var se *routeSwitchError
	if errors.As(cause, &se) && se.Code == routes.CodeSwitchBooking {
		// Nothing had changed yet; the record goes and the cause is the answer.
		routeSwitchRecords.Remove(r.name)
		return se
	}
	log.Printf("route-switch: %s: failed at %s: %v; rolling back", r.name, r.rec.Step, cause)
	r.rec.Error = cause.Error()
	r.rec.Step = rsRollback
	_ = routeSwitchRecords.Write(r.name, r.rec)
	relaunch, rerr := rollbackRouteSwitch(r.name, r.rec)
	cur, _ := session.ReadMeta(r.name)
	wire := wireSession(cur, routeSwitchAlive(cur))
	from := routeNameOf(r.rec.From)
	if from == "" {
		from = "its own default route"
	}
	msg := fmt.Sprintf("the switch to %s failed (%v) and was rolled back to %s", r.rec.To.Route, cause, from)
	switch {
	case rerr != nil:
		msg = fmt.Sprintf("the switch to %s failed (%v) and the rollback is incomplete (%v); it is retried when the Agent next starts", r.rec.To.Route, cause, rerr)
	case r.rec.WasAlive && relaunch != nil:
		msg += fmt.Sprintf("; the session could not be started again on it (%v) and is stopped", relaunch)
	case r.rec.WasAlive:
		msg += "; the session is running on it again"
	default:
		msg += "; the session is stopped, as it was"
	}
	return &routeSwitchError{Status: http.StatusBadGateway, Code: routes.CodeSwitchFailed, Message: msg, Session: &wire}
}

// rollbackRouteSwitch puts the session back as it was. It returns (relaunch, rollback): the
// error of starting the old runtime again, which leaves the session consistent but stopped, and
// the error of a step that must be retried, which keeps the record. Safe to run again.
func rollbackRouteSwitch(name string, rec routeSwitchRecord) (relaunch, rollback error) {
	cur, ok := session.ReadMeta(name)
	if !ok {
		routeSwitchRecords.Remove(name)
		return nil, nil
	}
	if switchStored(cur, rec) {
		// Whatever the new route started goes first, so the old selection is never stored beside a
		// process of the new one (and the new one never beside the old).
		if routeSwitchAlive(cur) {
			if _, err := routeSwitchHalt(cur); err != nil {
				return nil, fmt.Errorf("stopping the new runtime: %w", err)
			}
		}
		_, wrote, err := updateSessionMetaChecked(name, func(c *session.Meta) bool {
			kept := c.RouteSwitches[:0:0]
			for _, sw := range c.RouteSwitches {
				if sw.At != rec.At {
					kept = append(kept, sw)
				}
			}
			c.RouteSwitches = kept
			c.Route = rec.From.Clone()
			return true
		})
		// The new selection's tokens end with it; the old route's relaunch below mints its own.
		endRouteTokens(name)
		if err != nil || !wrote {
			if err == nil {
				err = errors.New("the session is gone or changed")
			}
			return nil, fmt.Errorf("restoring the old selection: %w", err)
		}
	}
	if rec.Booking != "" {
		episodeMu := rateLimitEpisodeLock(name)
		episodeMu.Lock()
		repairRateLimitBookingAfterMove(name, time.Now())
		episodeMu.Unlock()
	}
	if rec.WasAlive {
		if m, ok := session.ReadMeta(name); ok && !routeSwitchAlive(m) {
			if ref := startRefusalOf(m); ref != nil {
				relaunch = errors.New(ref.Message)
			} else {
				recallSettings(&m)
				if err := routeSwitchLaunch(m, false); err != nil {
					relaunch = err
				} else {
					UpdateSessionMeta(name, func(c *session.Meta) bool {
						if c.StoppedAt == "" {
							return false
						}
						c.StoppedAt = ""
						return true
					})
				}
			}
		}
	}
	routeSwitchRecords.Remove(name)
	return relaunch, nil
}

// switchStored reports whether the meta carries this switch's selection and boundary.
func switchStored(m session.Meta, rec routeSwitchRecord) bool {
	if rec.At == "" {
		return false
	}
	for _, sw := range m.RouteSwitches {
		if sw.At == rec.At {
			return true
		}
	}
	return false
}

// --- recovery ------------------------------------------------------------------------------

// RecoverRouteSwitches finishes the switches a previous process left, once at boot. The
// meta and the process decide, not the step alone, because the record can lag a write by one
// step: a switch whose selection is not stored is rolled back (nothing but the booking and
// perhaps a stopped runtime to put right); one that is stored and runs on the new route, or got
// as far as resumed, is completed; one that is stored without a runtime is rolled back, leaving
// the session stopped on the route it held. An old runtime stopped by the switch is not started
// again at boot: the stored selection is what a start will use, which is the invariant.
func RecoverRouteSwitches() int {
	ents, err := os.ReadDir(routeSwitchRecords.Dir())
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !session.ValidName(name) {
			continue
		}
		rec, ok := routeSwitchRecords.Read(name)
		if !ok {
			continue
		}
		recoverRouteSwitch(name, rec)
		n++
	}
	return n
}

func recoverRouteSwitch(name string, rec routeSwitchRecord) {
	unlock, ok := moveOpLock(name)
	if !ok {
		return
	}
	defer unlock()
	x, err := lifegate.Lock(context.Background(), name)
	if err != nil {
		return
	}
	defer x.Unlock()
	m, ok := session.ReadMeta(name)
	if !ok {
		routeSwitchRecords.Remove(name)
		return
	}
	stored := switchStored(m, rec)
	switch {
	case stored && rec.Step != rsRollback && (rec.Step == rsResumed || routeSwitchAlive(m)):
		log.Printf("route-switch: %s: completing a switch to %s the previous process left at %s", name, rec.To.Route, rec.Step)
		finishRouteSwitch(name, rec)
	default:
		log.Printf("route-switch: %s: rolling back a switch to %s the previous process left at %s", name, rec.To.Route, rec.Step)
		rec.WasAlive = false // never start a runtime at boot
		if _, rerr := rollbackRouteSwitch(name, rec); rerr != nil {
			log.Printf("route-switch: %s: rollback incomplete, kept for the next start: %v", name, rerr)
		}
	}
}

// --- the cancelled booking -------------------------------------------------------------------

// refuseCancelledResume reports that a scheduled usage-limit resume must not be delivered
// because its booking was cancelled: the Control Plane's delete may not have reached the
// scheduler before it fired. Two things make a booking dead: its id is recorded for cancellation,
// or the session has switched route and its episode no longer names the id. Without this a
// "the limit has reset, carry on" that was in flight when a switch started would be typed into
// the session on the metered route.
func refuseCancelledResume(name, source, prompt, scheduleID string) bool {
	if scheduleID == "" || injectionSource(source) != TurnSourceSchedule || !isRateLimitResumePrompt(prompt) {
		return false
	}
	if rateLimitCancelRecorded(scheduleID) {
		return true
	}
	m, ok := session.ReadMeta(name)
	if !ok || len(m.RouteSwitches) == 0 {
		return false
	}
	st, has := RateLimitStates.Read(name)
	return !has || st.ScheduleID != scheduleID
}
