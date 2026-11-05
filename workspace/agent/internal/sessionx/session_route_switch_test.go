package sessionx

// The switch away from a stopped subscription (ADR 0128 decision 10): the order of its steps, the
// refusals, the rollback, and recovery from a crash at every step boundary. The real claude
// adapter answers the route questions (against an isolated config directory and key store); the
// process, the transcript and the CP are stand-ins.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/lifegate"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

type switchEnv struct {
	t      *testing.T
	name   string
	alive  bool
	events []string
	// atLimit is what the transcript says: kind and whether the session is at it.
	limitKind agents.LimitKind
	atLimit   bool
	state     string
	haltErr   error
	// dropErr is what the CP answers a delete (nil = confirmed).
	dropErr error
	// launchErr answers launches in order; a nil entry (or none left) succeeds.
	launchErr []error
	launches  []session.Meta
}

func newSwitchEnv(t *testing.T, route *routes.Selection) *switchEnv {
	t.Helper()
	isolateAgentState(t)
	setRateLimitPref(t, true)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	setAnthropicKey(t, "sk-ant-api03-switch-test")
	e := &switchEnv{t: t, name: "sw1", alive: true, limitKind: agents.LimitWindow, atLimit: true, state: agents.StateLimited}
	session.WriteMeta(session.Meta{Name: e.name, Kind: session.KindClaude, Dir: t.TempDir(), Route: route,
		CreatedAt: "2026-11-05T10:00:00+09:00"})
	if err := RateLimitStates.Write(e.name, rateLimitState{
		At: time.Now().Add(-time.Hour).Format(time.RFC3339), ResumeAt: time.Now().Add(2 * time.Hour).Format(time.RFC3339),
		ScheduleID: "sch_old", ScheduleTries: 1,
	}); err != nil {
		t.Fatal(err)
	}
	// Events from the CP, in order: the delete of the old booking, and any booking made again.
	prevDrop, prevPut := dropRateLimitSchedule, putRateLimitSchedule
	dropRateLimitSchedule = func(id string) error { e.events = append(e.events, "drop:"+id); return e.dropErr }
	putRateLimitSchedule = func(_ session.Meta, _ time.Time) (string, error) {
		e.events = append(e.events, "put")
		return "sch_new", nil
	}
	prevAt, prevHalt, prevLaunch, prevAlive, prevState := routeSwitchAtLimit, routeSwitchHalt, routeSwitchLaunch, routeSwitchAlive, moveSourceState
	routeSwitchAtLimit = func(session.Meta) (agents.LimitKind, bool) { return e.limitKind, e.atLimit }
	moveSourceState = func(session.Meta) (string, string) { return e.state, "" }
	routeSwitchAlive = func(session.Meta) bool { return e.alive }
	routeSwitchHalt = func(m session.Meta) (session.Meta, error) {
		e.events = append(e.events, "halt")
		if e.haltErr != nil {
			return m, e.haltErr
		}
		e.alive = false
		return m, nil
	}
	routeSwitchLaunch = func(m session.Meta, _ bool) error {
		e.events = append(e.events, "launch:"+routeNameOf(m.Route))
		e.launches = append(e.launches, m)
		if len(e.launchErr) > 0 {
			err := e.launchErr[0]
			e.launchErr = e.launchErr[1:]
			if err != nil {
				return err
			}
		}
		e.alive = true
		return nil
	}
	t.Cleanup(func() {
		dropRateLimitSchedule, putRateLimitSchedule = prevDrop, prevPut
		routeSwitchAtLimit, routeSwitchHalt, routeSwitchLaunch, routeSwitchAlive, moveSourceState = prevAt, prevHalt, prevLaunch, prevAlive, prevState
		routeSwitchAfterStep = nil
		lifegate.ResetForTest()
	})
	return e
}

func (e *switchEnv) meta() session.Meta {
	e.t.Helper()
	m, ok := session.ReadMeta(e.name)
	if !ok {
		e.t.Fatal("no meta")
	}
	return m
}

func (e *switchEnv) run() (*routeSwitchWire, error) {
	return runRouteSwitch(context.Background(), e.name, "")
}

func (e *switchEnv) code(err error) string {
	var se *routeSwitchError
	if !errors.As(err, &se) {
		return ""
	}
	return se.Code
}

var subscriptionRoute = &routes.Selection{Route: "anthropic-subscription"}

func indexOf(events []string, want string) int {
	for i, ev := range events {
		if ev == want {
			return i
		}
	}
	return -1
}

func TestRouteSwitchRunsEveryStepInOrder(t *testing.T) {
	e := newSwitchEnv(t, subscriptionRoute)
	res, err := e.run()
	if err != nil {
		t.Fatal(err)
	}
	m := e.meta()
	if m.Route == nil || m.Route.Route != "anthropic-api" || !strings.HasPrefix(m.Route.Connection, "ANTHROPIC_API_KEY@") {
		t.Fatalf("stored selection = %+v, want the key-bound anthropic-api", m.Route)
	}
	if len(m.RouteSwitches) != 1 || m.RouteSwitches[0].From == nil || m.RouteSwitches[0].From.Route != "anthropic-subscription" ||
		m.RouteSwitches[0].To.Route != "anthropic-api" || m.RouteSwitches[0].At == "" {
		t.Fatalf("boundary = %+v", m.RouteSwitches)
	}
	// The old booking is deleted at the CP BEFORE the old runtime is stopped, which is before the
	// new one starts: a stale "carry on" can never find the metered route.
	drop, halt, launch := indexOf(e.events, "drop:sch_old"), indexOf(e.events, "halt"), indexOf(e.events, "launch:anthropic-api")
	if drop < 0 || halt < 0 || launch < 0 || !(drop < halt && halt < launch) {
		t.Fatalf("events = %v, want drop < halt < launch", e.events)
	}
	if _, has := RateLimitStates.Read(e.name); has {
		t.Error("the usage-limit episode of the old route is still open")
	}
	if _, left := routeSwitchRecords.Read(e.name); left {
		t.Error("the switch record outlived the switch")
	}
	if res.To != "anthropic-api" || res.From != "anthropic-subscription" || res.BookingCancelPending {
		t.Errorf("answer = %+v", res)
	}
	if !e.alive {
		t.Error("the session is not running after the switch")
	}
}

func TestRouteSwitchOfAStoppedSessionStartsIt(t *testing.T) {
	e := newSwitchEnv(t, subscriptionRoute)
	e.alive, e.state = false, "stopped"
	if _, err := e.run(); err != nil {
		t.Fatal(err)
	}
	if indexOf(e.events, "halt") >= 0 {
		t.Errorf("a stopped session was halted: %v", e.events)
	}
	if !e.alive || e.meta().Route.Route != "anthropic-api" {
		t.Errorf("alive=%v route=%+v", e.alive, e.meta().Route)
	}
}

// The refusals. Each leaves the session exactly as it was and stops nothing.
func TestRouteSwitchRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		route *routes.Selection
		setup func(e *switchEnv)
		want  string
	}{
		"spend limit":      {subscriptionRoute, func(e *switchEnv) { e.limitKind = agents.LimitSpend }, routes.CodeSwitchSpendLimit},
		"no limit":         {subscriptionRoute, func(e *switchEnv) { e.atLimit = false }, routes.CodeSwitchNoLimit},
		"api route":        {&routes.Selection{Route: "anthropic-api"}, nil, routes.CodeSwitchNotSubscription},
		"working":          {subscriptionRoute, func(e *switchEnv) { e.state = "working" }, routes.CodeSwitchState},
		"no key":           {subscriptionRoute, func(e *switchEnv) { setAnthropicKey(e.t, "") }, routes.CodeRouteUnavailable},
		"empty, env token": {nil, func(e *switchEnv) { e.t.Setenv("ANTHROPIC_API_KEY", "sk-ant-env") }, routes.CodeSwitchUnproven},
		"empty, no login":  {nil, nil, routes.CodeSwitchUnproven}, // no OAuth record in the isolated config dir
		"wrong target": {subscriptionRoute, func(e *switchEnv) {
			_, err := runRouteSwitch(context.Background(), e.name, "bedrock")
			if e.code(err) != routes.CodeSwitchUnsupported {
				e.t.Errorf("target bedrock: %v", err)
			}
		}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			e := newSwitchEnv(t, tc.route)
			if tc.setup != nil {
				tc.setup(e)
			}
			if tc.want == "" {
				return
			}
			before := e.meta()
			_, err := e.run()
			if got := e.code(err); got != tc.want {
				t.Fatalf("code = %q (%v), want %q", got, err, tc.want)
			}
			if len(e.events) != 0 {
				t.Errorf("a refused switch did something: %v", e.events)
			}
			if after := e.meta(); routeNameOf(after.Route) != routeNameOf(before.Route) || len(after.RouteSwitches) != 0 {
				t.Errorf("a refused switch changed the meta: %+v", after.Route)
			}
			if st, _ := RateLimitStates.Read(e.name); st.ScheduleID != "sch_old" {
				t.Errorf("a refused switch touched the booking: %+v", st)
			}
		})
	}
}

// Decision 2/6: an empty selection is never switched, not even with a clean environment and a
// live login on disk: an API session that outlived an Agent restart looks exactly like that.
func TestRouteSwitchRefusesAnEmptySelectionEvenWithALiveLogin(t *testing.T) {
	e := newSwitchEnv(t, nil)
	writeOAuthLogin(t)
	_, err := e.run()
	if e.code(err) != routes.CodeSwitchUnproven {
		t.Fatalf("err = %v, want %s", err, routes.CodeSwitchUnproven)
	}
	if len(e.events) != 0 {
		t.Errorf("a refused switch did something: %v", e.events)
	}
}

func TestRouteSwitchOfAnotherKindIsRefused(t *testing.T) {
	isolateAgentState(t)
	session.WriteMeta(session.Meta{Name: "cx1", Kind: session.KindCodex, Dir: t.TempDir()})
	_, err := runRouteSwitch(context.Background(), "cx1", "")
	var se *routeSwitchError
	if !errors.As(err, &se) || se.Code != routes.CodeSwitchUnsupported {
		t.Fatalf("codex: %v", err)
	}
}

// A failure after the old runtime was stopped rolls back: the old selection is restored, the old
// runtime is started again, and the booking is made again, so the limit is still watched.
func TestRouteSwitchRollsBackWhenTheNewRouteWillNotStart(t *testing.T) {
	e := newSwitchEnv(t, subscriptionRoute)
	e.launchErr = []error{errors.New("tmux: no server")} // the first launch (new route) fails
	_, err := e.run()
	if e.code(err) != routes.CodeSwitchFailed || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("err = %v", err)
	}
	m := e.meta()
	if m.Route == nil || m.Route.Route != "anthropic-subscription" || len(m.RouteSwitches) != 0 {
		t.Fatalf("selection after rollback = %+v switches=%+v", m.Route, m.RouteSwitches)
	}
	if got := e.launches; len(got) != 2 || routeNameOf(got[0].Route) != "anthropic-api" || routeNameOf(got[1].Route) != "anthropic-subscription" {
		t.Fatalf("launches = %+v, want the failed new route then the old one", routes0(got))
	}
	if !e.alive {
		t.Error("the session was running before and is not now")
	}
	st, has := RateLimitStates.Read(e.name)
	if !has || st.ScheduleID != "sch_new" {
		t.Errorf("episode after rollback = %+v, want the resume booked again as sch_new", st)
	}
	if _, left := routeSwitchRecords.Read(e.name); left {
		t.Error("the record outlived a completed rollback")
	}
}

// A fire of the booking the switch cancelled that was waiting on the gate while the switch failed
// and rolled back is refused even after the cleanup sweep has run: the episode now names the
// rebooked sch_new, and no switch history is left to say sch_old is dead.
func TestRouteSwitchRollbackKeepsTheOldBookingRefusedThroughCleanup(t *testing.T) {
	e := newSwitchEnv(t, subscriptionRoute)
	e.launchErr = []error{errors.New("tmux: no server")}
	if _, err := e.run(); e.code(err) != routes.CodeSwitchFailed {
		t.Fatalf("err = %v", err)
	}
	if st, _ := RateLimitStates.Read(e.name); st.ScheduleID != "sch_new" {
		t.Fatalf("episode = %+v", st)
	}
	retryRateLimitScheduleCleanups()
	if !refuseCancelledResume(e.name, TurnSourceSchedule, rateLimitResumePromptFor("en"), "sch_old") {
		t.Error("the cancelled booking was accepted after the rollback and a cleanup sweep")
	}
	if refuseCancelledResume(e.name, TurnSourceSchedule, rateLimitResumePromptFor("en"), "sch_new") {
		t.Error("the current booking was refused")
	}
}

func routes0(ms []session.Meta) []string {
	var out []string
	for _, m := range ms {
		out = append(out, routeNameOf(m.Route))
	}
	return out
}

// Risk 2: the rollback never leaves the new route stored beside the old runtime, nor the old
// selection beside a runtime of the new one, even when the old runtime cannot be restarted.
func TestRouteSwitchRollbackThatCannotRestartLeavesTheOldSelectionStopped(t *testing.T) {
	e := newSwitchEnv(t, subscriptionRoute)
	e.launchErr = []error{errors.New("boom"), errors.New("boom again")}
	_, err := e.run()
	if e.code(err) != routes.CodeSwitchFailed || !strings.Contains(err.Error(), "is stopped") {
		t.Fatalf("err = %v", err)
	}
	if e.alive {
		t.Error("a runtime is up")
	}
	if m := e.meta(); m.Route == nil || m.Route.Route != "anthropic-subscription" || len(m.RouteSwitches) != 0 {
		t.Errorf("selection = %+v", m.Route)
	}
}

func TestRouteSwitchRollsBackWhenTheOldRuntimeWillNotStop(t *testing.T) {
	e := newSwitchEnv(t, subscriptionRoute)
	e.haltErr = errors.New("tmux: cannot kill")
	_, err := e.run()
	if e.code(err) != routes.CodeSwitchFailed {
		t.Fatalf("err = %v", err)
	}
	if m := e.meta(); m.Route.Route != "anthropic-subscription" || len(m.RouteSwitches) != 0 {
		t.Errorf("selection = %+v", m.Route)
	}
	if indexOf(e.events, "launch:anthropic-api") >= 0 {
		t.Errorf("the new route started beside an old runtime that would not stop: %v", e.events)
	}
}

// A booking that cannot be recorded as cancelled fails the switch before anything changes.
func TestRouteSwitchFailsVisiblyWhenTheBookingCannotBeCancelled(t *testing.T) {
	e := newSwitchEnv(t, subscriptionRoute)
	// A regular file where the cleanup store's directory belongs: the record cannot be written.
	dir := rateLimitCleanups.Dir()
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := e.run()
	if e.code(err) != routes.CodeSwitchBooking {
		t.Fatalf("err = %v, want %s", err, routes.CodeSwitchBooking)
	}
	if len(e.events) != 0 || !e.alive {
		t.Errorf("something changed: events=%v alive=%v", e.events, e.alive)
	}
	if st, _ := RateLimitStates.Read(e.name); st.ScheduleID != "sch_old" {
		t.Errorf("episode = %+v", st)
	}
	if _, left := routeSwitchRecords.Read(e.name); left {
		t.Error("record left behind")
	}
}

// The CP did not confirm the delete: the switch goes on, says so, and a fire that arrives first
// is refused.
func TestRouteSwitchWithAnUnconfirmedCancelRefusesTheStaleFire(t *testing.T) {
	e := newSwitchEnv(t, subscriptionRoute)
	e.dropErr = errors.New("cp unreachable")
	res, err := e.run()
	if err != nil {
		t.Fatal(err)
	}
	if !res.BookingCancelPending {
		t.Error("an unconfirmed delete was not reported")
	}
	if !refuseCancelledResume(e.name, TurnSourceSchedule, rateLimitResumePromptFor("en"), "sch_old") {
		t.Error("the stale booking would still be delivered to the metered route")
	}
	// Controls: another schedule, an ordinary scheduled prompt, a member's message.
	if refuseCancelledResume(e.name, TurnSourceSchedule, "run the nightly report", "sch_old") {
		t.Error("an ordinary scheduled prompt was refused")
	}
	if refuseCancelledResume(e.name, "", "hello", "") {
		t.Error("a member's message was refused")
	}
}

// After a switch the episode is gone, so a resume booking that is not the session's current one
// is dead even once the cancel record has been forgotten.
func TestRouteSwitchedSessionRefusesAnyResumeBookingItDoesNotHold(t *testing.T) {
	e := newSwitchEnv(t, subscriptionRoute)
	if _, err := e.run(); err != nil {
		t.Fatal(err)
	}
	finishRateLimitCancel("sch_old") // the record is forgotten once the episode let go
	if !refuseCancelledResume(e.name, TurnSourceSchedule, rateLimitResumePromptFor("ja"), "sch_old") {
		t.Error("a booking of the old route is delivered after the switch")
	}
}

// Risk 1: input waits at the gate for the whole switch and lands after it.
func TestRouteSwitchHoldsInputAtTheGate(t *testing.T) {
	e := newSwitchEnv(t, subscriptionRoute)
	inHalt, release := make(chan struct{}), make(chan struct{})
	routeSwitchHalt = func(m session.Meta) (session.Meta, error) {
		close(inHalt)
		<-release
		e.alive = false
		return m, nil
	}
	done := make(chan error, 1)
	go func() { _, err := e.run(); done <- err }()
	<-inHalt
	entered := make(chan routeOfMeta, 1)
	go func() {
		l, err := lifegate.Enter(context.Background(), e.name)
		if err != nil {
			entered <- routeOfMeta{err: err}
			return
		}
		defer l.Release()
		m, _ := session.ReadMeta(e.name)
		entered <- routeOfMeta{route: routeNameOf(m.Route)}
	}()
	select {
	case r := <-entered:
		t.Fatalf("input was admitted in the middle of the switch: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-entered:
		if r.err != nil || r.route != "anthropic-api" {
			t.Errorf("input landed on %+v, want the new route", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("input never got through after the switch")
	}
}

type routeOfMeta struct {
	route string
	err   error
}

// A second switch while one runs is refused, and so is one after it (the session is no longer on
// a subscription).
func TestRouteSwitchIsNotRepeatable(t *testing.T) {
	e := newSwitchEnv(t, subscriptionRoute)
	if _, err := e.run(); err != nil {
		t.Fatal(err)
	}
	e.events = nil
	_, err := e.run()
	if e.code(err) != routes.CodeSwitchNotSubscription || len(e.events) != 0 {
		t.Errorf("second switch: %v events=%v", err, e.events)
	}
}

// A crash at every step boundary. The process dies right after the step is persisted; recovery
// at the next start settles it. The invariant, whatever the step: no record is left, and the
// stored selection is the route a start of the session would use, and matches a running pane.
func TestRouteSwitchCrashAtEveryStepBoundary(t *testing.T) {
	for _, tc := range []struct {
		step string
		// want: the route stored after recovery, whether the session runs, and the booking.
		route     string
		alive     bool
		switches  int
		bookingID string // the episode's booking after recovery ("" = none)
	}{
		// Nothing but the record exists: the booking is intact, the session untouched.
		{rsBegun, "anthropic-subscription", true, 0, "sch_old"},
		// The booking id is in the record but its cancel was not recorded yet.
		{rsBooking, "anthropic-subscription", true, 0, "sch_old"},
		// The old runtime is stopped and the booking cancelled: rolled back, resume booked again,
		// the runtime is NOT started at boot.
		{rsStopped, "anthropic-subscription", false, 0, "sch_new"},
		// The new selection is stored and no runtime started: rolled back, stopped.
		{rsStored, "anthropic-subscription", false, 0, "sch_new"},
		// The new route runs: completed.
		{rsResumed, "anthropic-api", true, 1, ""},
	} {
		t.Run(tc.step, func(t *testing.T) {
			e := newSwitchEnv(t, subscriptionRoute)
			routeSwitchAfterStep = func(step string) error {
				if step == tc.step {
					return errRouteSwitchCrash
				}
				return nil
			}
			if _, err := e.run(); !errors.Is(err, errRouteSwitchCrash) {
				t.Fatalf("run = %v, want the simulated crash", err)
			}
			if _, has := routeSwitchRecords.Read(e.name); !has {
				t.Fatal("the crash left no record to recover from")
			}
			routeSwitchAfterStep = nil
			lifegate.ResetForTest() // a restart forgets the gates
			if n := RecoverRouteSwitches(); n != 1 {
				t.Fatalf("recovered %d", n)
			}
			m := e.meta()
			if routeNameOf(m.Route) != tc.route || len(m.RouteSwitches) != tc.switches {
				t.Errorf("route=%q switches=%d, want %q/%d", routeNameOf(m.Route), len(m.RouteSwitches), tc.route, tc.switches)
			}
			if e.alive != tc.alive {
				t.Errorf("alive=%v, want %v", e.alive, tc.alive)
			}
			// The runtime and the stored route agree: a pane that runs is on the stored route.
			if e.alive && len(e.launches) > 0 && routeNameOf(e.launches[len(e.launches)-1].Route) != routeNameOf(m.Route) {
				t.Errorf("the running pane is on %q but %q is stored", routeNameOf(e.launches[len(e.launches)-1].Route), routeNameOf(m.Route))
			}
			if _, left := routeSwitchRecords.Read(e.name); left {
				t.Error("the record outlived recovery")
			}
			st, has := RateLimitStates.Read(e.name)
			if got := st.ScheduleID; got != tc.bookingID || has != (tc.bookingID != "") {
				t.Errorf("episode = %+v has=%v, want booking %q", st, has, tc.bookingID)
			}
			// A second recovery finds nothing.
			if n := RecoverRouteSwitches(); n != 0 {
				t.Errorf("a second recovery settled %d", n)
			}
		})
	}
}

func TestRouteSwitchPreviewOffersOnlyWhatTheSwitchWouldAccept(t *testing.T) {
	e := newSwitchEnv(t, subscriptionRoute)
	get := func() routeSwitchPreview {
		req := httptest.NewRequest(http.MethodGet, "/sessions/sw1/route-switch", nil)
		req.SetPathValue("name", e.name)
		rec := httptest.NewRecorder()
		HandleSessionRouteSwitchPreview(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		var p routeSwitchPreview
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := get()
	if !p.Allowed || p.To != "anthropic-api" || !p.MayCostMore || !p.CacheNotShared || p.AccountSettingURL == "" || !p.AccountSettingAffectsAll {
		t.Errorf("allowed preview = %+v", p)
	}
	e.limitKind = agents.LimitSpend
	p = get()
	if p.Allowed || p.Code != routes.CodeSwitchSpendLimit || p.MayCostMore || p.AccountSettingURL != "" {
		t.Errorf("spend-limit preview = %+v", p)
	}
	e.limitKind = agents.LimitWindow
	if err := routeSwitchRecords.Write(e.name, routeSwitchRecord{Session: e.name, Step: rsStopped}); err != nil {
		t.Fatal(err)
	}
	if p = get(); p.Allowed || !p.Pending {
		t.Errorf("preview with an unfinished switch = %+v", p)
	}
	if len(e.events) != 0 {
		t.Errorf("a preview did something: %v", e.events)
	}
}

// writeOAuthLogin stores a live claude.ai login in the isolated config directory.
func writeOAuthLogin(t *testing.T) {
	t.Helper()
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	now := time.Now()
	creds := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"tok","expiresAt":%d,"refreshTokenExpiresAt":%d}}`,
		now.Add(time.Hour).UnixMilli(), now.Add(24*time.Hour).UnixMilli())
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A rate-limit sweep step that took its meta before the switch must judge the session as it is
// once it holds the gate: the old route's abort, still the transcript's tail, is not a limit of
// the new route, and no resume may be booked against it.
func TestRateLimitTickThatStraddlesTheSwitchBooksNothing(t *testing.T) {
	e := newSwitchEnv(t, subscriptionRoute)
	stale := e.meta()
	if _, err := e.run(); err != nil {
		t.Fatal(err)
	}
	e.events = nil
	// A live pane and the old route's abort at the transcript's tail, recorded before the boundary.
	if o, err := tmuxx.Cmd("new-session", "-d", "-s", session.TmuxName(e.name), "sleep 120").CombinedOutput(); err != nil {
		t.Skipf("no tmux: %v %s", err, o)
	}
	dir := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", "-proj")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	abort := fmt.Sprintf(`{"type":"assistant","isApiErrorMessage":true,"apiErrorStatus":429,"timestamp":%q,"message":{"content":[{"type":"text","text":"You've hit your 5-hour limit · resets 1:50am (Asia/Tokyo)"}]}}`,
		time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(filepath.Join(dir, session.UUID(stale.Dir, stale.Name)+".jsonl"),
		[]byte(`{"type":"user","message":{"content":"go"}}`+"\n"+abort+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, atLimit := atUsageLimit(stale); !atLimit {
		t.Fatal("fixture: the stale meta should read the old abort as a limit")
	}
	rateLimitTickSession(stale, time.Now())
	if st, has := RateLimitStates.Read(e.name); has {
		t.Errorf("the tick recreated an episode for the new route: %+v", st)
	}
	if indexOf(e.events, "put") >= 0 {
		t.Errorf("a resume was booked against the old route's limit: %v", e.events)
	}
}

// A switch that cannot finish its rollback keeps the session's input closed until recovery
// settles it, so no turn runs between and none is filed under the wrong route.
func TestRouteSwitchIncompleteRollbackKeepsInputClosed(t *testing.T) {
	e := newSwitchEnv(t, subscriptionRoute)
	routeSwitchAfterStep = func(step string) error {
		if step == rsResumed {
			e.haltErr = errors.New("cannot stop the new process") // the rollback's halt will fail
			return errors.New("persisting the resumed step failed")
		}
		return nil
	}
	_, err := e.run()
	if !strings.Contains(fmt.Sprint(err), "incomplete") {
		t.Fatalf("err = %v, want an incomplete rollback", err)
	}
	if _, left := routeSwitchRecords.Read(e.name); !left {
		t.Fatal("the record of an incomplete rollback was dropped")
	}
	var held *lifegate.HeldError
	if _, err := lifegate.Enter(context.Background(), e.name); !errors.As(err, &held) {
		t.Fatalf("input was admitted to a session whose switch is unsettled: %v", err)
	}
	if !lifegate.Closed(e.name) {
		t.Error("Closed says open")
	}
	// Boot recovery that works settles it and opens the gate again.
	routeSwitchAfterStep = nil
	e.haltErr = nil
	if n := RecoverRouteSwitches(); n != 1 {
		t.Fatalf("recovered %d", n)
	}
	l, err := lifegate.Enter(context.Background(), e.name)
	if err != nil {
		t.Fatalf("input still closed after recovery: %v", err)
	}
	l.Release()
	if m := e.meta(); routeNameOf(m.Route) != "anthropic-subscription" || len(m.RouteSwitches) != 0 {
		t.Errorf("after recovery route=%+v switches=%d", m.Route, len(m.RouteSwitches))
	}
}
