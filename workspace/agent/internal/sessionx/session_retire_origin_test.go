package sessionx

// What starts a retired session again (ADR 0137 §6, note of 2026-11-16): a deliberate act, by the
// member or by one of the member's own sessions or connections. What fires by itself is refused
// with 409 session_retired and leaves the mark. One test per caller of POST /start and of the
// Managed resume, in Terminal (CLI) and in Managed.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// retiredStopped makes the session "s" that an overdue run retired and stopped.
func (c *carriedEnv) retiredStopped(t *testing.T, k carriedKind) session.Meta {
	t.Helper()
	m := session.Meta{Name: "s", Kind: k.kind, Driver: k.driver, Dir: t.TempDir()}
	session.WriteMeta(m)
	if code, out := postRetireIf(t, "s", -1); code != http.StatusOK || !reportOf(out, "s").Stopped {
		t.Fatalf("retire = %d %+v", code, out)
	}
	if cur, _ := session.ReadMeta("s"); cur.RetiredAt == "" {
		t.Fatal("not retired")
	}
	return m
}

func (c *carriedEnv) start(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/sessions/s/start"+query, nil)
	req.SetPathValue("name", "s")
	rec := httptest.NewRecorder()
	HandleStartSession(rec, req)
	return rec
}

func (c *carriedEnv) wantStarted(t *testing.T, k carriedKind, what string) {
	t.Helper()
	cur, _ := session.ReadMeta("s")
	if cur.RetiredAt != "" || cur.RetireClearedID == "" || cur.PersonActMark == 0 {
		t.Fatalf("%s: retiredAt=%q clearedID=%q actMark=%d, want the mark off and an act recorded", what, cur.RetiredAt, cur.RetireClearedID, cur.PersonActMark)
	}
	if k.driver == "" && !SessionAlive(cur) {
		t.Fatalf("%s: the session was not launched", what)
	}
}

// wantRefused: 409 session_retired, the mark kept, nothing launched or delivered, and no act
// recorded by a start (a caller that records its own act on arrival, wantRefusedKeepsMark, is
// judged on the mark alone).
func (c *carriedEnv) wantRefused(t *testing.T, k carriedKind, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	c.refused(t, k, rec, what, true)
}

func (c *carriedEnv) wantRefusedKeepsMark(t *testing.T, k carriedKind, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	c.refused(t, k, rec, what, false)
}

func (c *carriedEnv) refused(t *testing.T, k carriedKind, rec *httptest.ResponseRecorder, what string, noAct bool) {
	t.Helper()
	// A Terminal (CLI) session is never started by /input: not_running is its refusal.
	if rec.Code != http.StatusConflict || !(strings.Contains(rec.Body.String(), "session_retired") ||
		(k.driver == "" && strings.Contains(rec.Body.String(), "not_running"))) {
		t.Fatalf("%s = %d %s, want 409 session_retired", what, rec.Code, rec.Body)
	}
	cur, _ := session.ReadMeta("s")
	if cur.RetiredAt == "" || (noAct && cur.PersonActMark != 0) {
		t.Fatalf("%s: retiredAt=%q actMark=%d, want the mark kept and no act", what, cur.RetiredAt, cur.PersonActMark)
	}
	if k.driver == "" && SessionAlive(cur) {
		t.Fatalf("%s: the session was launched", what)
	}
	select {
	case in := <-c.handle.got:
		t.Fatalf("%s: delivered %+v", what, in)
	default:
	}
}

// POST /start: a caller that says nothing (the Console's Start through the Control Plane's relay,
// resume_session, a peer message to a stopped Terminal session, an older Control Plane) is a
// person's, whatever else is on the query; ?origin=automatic is refused on a retired session and
// records no act on any other.
func TestStartOriginDecidesWhetherARetiredSessionStarts(t *testing.T) {
	for _, k := range carriedKinds {
		t.Run(k.label, func(t *testing.T) {
			for _, q := range []string{"", "?force=0", "?origin=", "?origin=person", "?origin=Automatic"} {
				c := newCarriedEnv(t, "")
				c.retiredStopped(t, k)
				if rec := c.start(t, q); rec.Code != http.StatusOK {
					t.Fatalf("start%s = %d %s", q, rec.Code, rec.Body)
				}
				c.wantStarted(t, k, "start"+q)
			}
			c := newCarriedEnv(t, "")
			c.retiredStopped(t, k)
			c.wantRefused(t, k, c.start(t, "?origin=automatic"), "automatic start")
			c.wantRefused(t, k, c.start(t, "?fresh=0&origin=automatic"), "automatic start with other parameters")
		})
	}
}

// An automatic start of a session that is not retired goes ahead and is not an act.
func TestAutomaticStartOfAnOrdinarySessionIsNotAnAct(t *testing.T) {
	for _, k := range carriedKinds {
		t.Run(k.label, func(t *testing.T) {
			c := newCarriedEnv(t, "")
			c.retiredStopped(t, k)
			if err := clearRetired("s"); err != nil { // stopped, and no longer marked
				t.Fatal(err)
			}
			if rec := c.start(t, "?origin=automatic"); rec.Code != http.StatusOK {
				t.Fatalf("automatic start = %d %s", rec.Code, rec.Body)
			}
			if cur, _ := session.ReadMeta("s"); cur.PersonActMark != 0 {
				t.Fatalf("an automatic start recorded an act: %d", cur.PersonActMark)
			}
		})
	}
}

// The Agent's own automatic callers of the start route stay refused on a retired session: a
// watch's wake of its owner and a pull request notice (startSessionInProcess), and a plan-review
// reviewer's start (prepareReviewerSession).
func TestAgentsOwnAutomaticStartsRefuseARetiredSession(t *testing.T) {
	for _, k := range carriedKinds {
		t.Run(k.label, func(t *testing.T) {
			c := newCarriedEnv(t, "")
			c.retiredStopped(t, k)
			if err := startSessionInProcess("s"); err == nil {
				t.Fatal("startSessionInProcess started a retired session")
			}
			err := prepareReviewerSession("s")
			if err == nil {
				t.Fatal("a reviewer start took a retired session")
			}
			cur, _ := session.ReadMeta("s")
			if cur.RetiredAt == "" || cur.PersonActMark != 0 || (k.driver == "" && SessionAlive(cur)) {
				t.Fatalf("retiredAt=%q actMark=%d alive=%v", cur.RetiredAt, cur.PersonActMark, SessionAlive(cur))
			}
		})
	}
}

// A launch that fails leaves the mark as it was and records... the act that was decided on (the
// act is recorded before the launch, as in /start), but never takes the mark off.
func TestDeliberateStartWithAFailedLaunchLeavesTheMark(t *testing.T) {
	t.Run("Managed input", func(t *testing.T) {
		c := newCarriedEnv(t, "")
		c.retiredStopped(t, carriedKinds[1])
		useLockingDriver(t, session.KindCodex, &lockingDriver{resumeErr: os.ErrInvalid})
		rec := postTo(t, HandleSessionInput, "s", "input", `{"prompt":"go on"}`)
		if rec.Code < 500 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		if cur, _ := session.ReadMeta("s"); cur.RetiredAt == "" {
			t.Fatal("the mark was taken off by a launch that failed")
		}
	})
	t.Run("Terminal start", func(t *testing.T) {
		c := newCarriedEnv(t, "exit 1;")
		c.retiredStopped(t, carriedKinds[0])
		if rec := c.start(t, ""); rec.Code < 500 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		if cur, _ := session.ReadMeta("s"); cur.RetiredAt == "" {
			t.Fatal("the mark was taken off by a launch that failed")
		}
	})
}

// A mark that cannot be taken off after a Managed launch is /start's exception: the answer says so
// (retire_clear_failed) and nothing is claimed as delivered.
func TestManagedInputReportsAMarkItCouldNotClear(t *testing.T) {
	c := newCarriedEnv(t, "")
	c.retiredStopped(t, carriedKinds[1])
	useLockingDriver(t, session.KindCodex, &lockingDriver{onResume: func() { _ = os.Chmod(session.MetaDir(), 0o555) }})
	t.Cleanup(func() { _ = os.Chmod(session.MetaDir(), 0o755) })
	rec := postTo(t, HandleSessionInput, "s", "input", `{"prompt":"go on"}`)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "retire_clear_failed") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// A meta that cannot be read is in doubt: refused, nothing launched.
func TestManagedInputIsRefusedWhenTheMetaCannotBeRead(t *testing.T) {
	c := newCarriedEnv(t, "")
	m := c.retiredStopped(t, carriedKinds[1])
	if err := os.WriteFile(session.MetaPath("s"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	called := false
	useLockingDriver(t, session.KindCodex, &lockingDriver{onResume: func() { called = true }})
	d, _ := driverOf(m)
	if _, err := resumeManagedDeliberate(d, m); !errors.Is(err, errSessionRetired) || called {
		t.Fatalf("err=%v resumed=%v, want session_retired and no launch", err, called)
	}
}

// The origins of a message typed to a retired, stopped Managed session. Deliberate: the member's
// own, a peer's, an operator's, a connection's over /mcp. Automatic: a schedule (by any of its
// fields), a notice, an auto-resume, a queue, a spawn, an origin nobody knows.
func TestManagedInputOriginsAgainstARetiredSession(t *testing.T) {
	session.WriteMeta(session.Meta{Name: "peerfrom", Kind: session.KindClaude, Dir: t.TempDir()})
	cases := []struct {
		name, body string
		mcp        bool
		start      bool
	}{
		{"member", `{"prompt":"go on"}`, false, true},
		{"chat bridge discord", `{"prompt":"go on","source":"discord"}`, false, true},
		{"operator", `{"prompt":"go on","source":"operator"}`, false, true},
		{"operator with report_to", `{"prompt":"go on","report_to":"conv"}`, false, true},
		{"connection over /mcp", `{"prompt":"go on","connection":"c1"}`, true, true},
		{"peer", `{"prompt":"go on","peer_from":"peerfrom","peer_intent":"request"}`, false, true},
		{"schedule", `{"prompt":"go on","source":"schedule","schedule_id":"x","schedule_slot":"y"}`, false, false},
		{"schedule run-now", `{"prompt":"go on","source":"schedule-manual"}`, false, false},
		{"schedule fields alone", `{"prompt":"go on","schedule_id":"x"}`, false, false},
		{"notice", `{"prompt":"go on","source":"notice"}`, false, false},
		{"usage-limit resume", `{"prompt":"go on","source":"auto-resume"}`, false, false},
		{"queue", `{"prompt":"go on","source":"queue"}`, false, false},
		{"spawn", `{"prompt":"go on","source":"spawn"}`, false, false},
		{"unknown source", `{"prompt":"go on","source":"zzz"}`, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newCarriedEnv(t, "")
			session.WriteMeta(session.Meta{Name: "peerfrom", Kind: session.KindClaude, Dir: t.TempDir()})
			k := carriedKinds[1]
			c.retiredStopped(t, k)
			h := HandleSessionInput
			if tc.mcp {
				h = HandleSessionMCPInput
			}
			rec := postTo(t, h, "s", "input", tc.body)
			if !tc.start {
				c.wantRefused(t, k, rec, tc.name)
				return
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("%s = %d %s", tc.name, rec.Code, rec.Body)
			}
			select {
			case in := <-c.handle.got:
				if in.Prompt == "" {
					t.Fatal("empty delivery")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("nothing was delivered")
			}
			c.wantStarted(t, k, tc.name)
		})
	}
}

// A reviewer the Agent runs on its own for the planner (plan review with auto on) is not the
// member's session: its reply does not start a retired planner. Any other session's peer message does.
func TestPlanReviewerAutoReplyDoesNotStartARetiredPlanner(t *testing.T) {
	c := newCarriedEnv(t, "")
	k := carriedKinds[1]
	session.WriteMeta(session.Meta{Name: "rev", Kind: session.KindClaude, Dir: t.TempDir()})
	m := session.Meta{Name: "s", Kind: k.kind, Driver: k.driver, Dir: t.TempDir(),
		PlanReviewers: []session.PlanReviewer{{Reviewer: "rev", Auto: &session.PlanAuto{Enabled: true}}}}
	session.WriteMeta(m)
	if code, out := postRetireIf(t, "s", -1); code != http.StatusOK || !reportOf(out, "s").Stopped {
		t.Fatalf("retire = %d %+v", code, out)
	}
	rec := postTo(t, HandleSessionInput, "s", "input", `{"prompt":"APPROVE","peer_from":"rev","peer_intent":"answer"}`)
	c.wantRefused(t, k, rec, "an automatic reviewer's reply")
}

// The Console's own turn: start and steer are the member's message and start the session; an
// interrupt and a queue edit deliver nothing and never do.
func TestManagedTurnAgainstARetiredSession(t *testing.T) {
	k := carriedKinds[1]
	for _, op := range []string{"start", "steer"} {
		c := newCarriedEnv(t, "")
		c.retiredStopped(t, k)
		rec := postTo(t, HandleSessionTurn, "s", "turn", `{"op":"`+op+`","prompt":"go on"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", op, rec.Code, rec.Body)
		}
		c.wantStarted(t, k, op)
	}
	c := newCarriedEnv(t, "")
	c.retiredStopped(t, k)
	c.wantRefused(t, k, postTo(t, HandleSessionTurn, "s", "turn", `{"op":"interrupt"}`), "interrupt")
	// A queue edit looks at the live handle only, so it has nothing to start in the first place.
	for _, body := range []string{`{"op":"remove","id":"x"}`, `{"op":"dismiss_discard","id":"x"}`} {
		postTo(t, HandleSessionTurn, "s", "turn", body)
		if cur, _ := session.ReadMeta("s"); cur.RetiredAt == "" || cur.PersonActMark != 0 {
			t.Fatalf("%s: retiredAt=%q actMark=%d", body, cur.RetiredAt, cur.PersonActMark)
		}
	}
}

// The member's answer from the Console starts the session; a reply to an interaction by id, a
// settings read or change, and a chat-bridge button pick deliver no message and stay refused.
func TestManagedAnswerAndControlsAgainstARetiredSession(t *testing.T) {
	k := carriedKinds[1]
	c := newCarriedEnv(t, "")
	c.retiredStopped(t, k)
	useLockingDriver(t, session.KindCodex, &lockingDriver{})
	rec := postTo(t, HandleSessionAnswerQuestion, "s", "answer", `{"choices":[1]}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no_question") {
		t.Fatalf("answer = %d %s, want the session started and no_question", rec.Code, rec.Body)
	}
	c.wantStarted(t, k, "answer")

	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
		body string
	}{
		{"respond", HandleSessionRespond, `{"id":"i","decision":"allow"}`},
		{"settings", HandleSessionSettings, `{"model":"m"}`},
	} {
		c := newCarriedEnv(t, "")
		c.retiredStopped(t, k)
		useLockingDriver(t, session.KindCodex, &lockingDriver{})
		c.wantRefusedKeepsMark(t, k, postTo(t, tc.h, "s", tc.name, tc.body), tc.name)
	}
	c = newCarriedEnv(t, "")
	m := c.retiredStopped(t, k)
	useLockingDriver(t, session.KindCodex, &lockingDriver{})
	if _, err := answerManagedQuestion(m, bridge.ParsedInteraction{Kind: "q", Session: "s", QI: 0, OI: 1}, true); !errors.Is(err, errSessionRetired) {
		t.Fatalf("a bridge pick: %v", err)
	}
}

// The chat bridge's reply is the member's, a notice and an auto-resume are not.
func TestInjectSessionPromptOriginsAgainstARetiredSession(t *testing.T) {
	k := carriedKinds[1]
	for _, o := range []string{agents.OriginMember, agents.OriginDiscord, agents.OriginSlack} {
		c := newCarriedEnv(t, "")
		c.retiredStopped(t, k)
		if err := injectSessionPrompt("s", "go on", agents.Origin{Kind: o}); err != nil {
			t.Fatalf("%s: %v", o, err)
		}
		c.wantStarted(t, k, o)
	}
	for _, o := range []string{agents.OriginNotice, agents.OriginAutoResume, agents.OriginSchedule, agents.OriginQueue, ""} {
		c := newCarriedEnv(t, "")
		c.retiredStopped(t, k)
		if err := injectSessionPrompt("s", "go on", agents.Origin{Kind: o}); !errors.Is(err, errSessionRetired) {
			t.Fatalf("%q: %v, want session_retired", o, err)
		}
		cur, _ := session.ReadMeta("s")
		if cur.RetiredAt == "" || cur.PersonActMark != 0 {
			t.Fatalf("%q: retiredAt=%q actMark=%d", o, cur.RetiredAt, cur.PersonActMark)
		}
	}
}

// A deliberate message to a stopped session that is not retired starts it as before and records no
// act: only the start of a retired session is one.
func TestDeliberateInputToAnOrdinaryStoppedManagedSessionIsNotAnAct(t *testing.T) {
	newCarriedEnv(t, "")
	m := session.Meta{Name: "s", Kind: session.KindCodex, Driver: session.DriverManaged, Dir: t.TempDir()}
	session.WriteMeta(m)
	d, _ := driverOf(m)
	if _, err := resumeManagedDeliberate(d, m); err != nil {
		t.Fatal(err)
	}
	if cur, _ := session.ReadMeta("s"); cur.PersonActMark != 0 {
		t.Fatalf("act recorded: %d", cur.PersonActMark)
	}
}

// The execution-method switch and the route switch keep refusing a marked session.
// (TestSwitchesStillRefuseAMarkedSession in session_carried_retired_test.go.)

// Lock order and gate: a retire that holds the write side makes a deliberate start wait for it, and
// the start then finds the mark; a start that holds the read side makes the retire wait. Nothing
// is left marked and running.
func TestDeliberateManagedStartIsOrderedAgainstARetire(t *testing.T) {
	c := newCarriedEnv(t, "")
	k := carriedKinds[1]
	m := session.Meta{Name: "s", Kind: k.kind, Driver: k.driver, Dir: t.TempDir()}
	session.WriteMeta(m)
	var launched sync.WaitGroup
	launched.Add(1)
	release := make(chan struct{})
	var once sync.Once
	prev := managedDrivers[session.KindCodex]
	managedDrivers[session.KindCodex] = &gatedDriver{Driver: prev, h: actHandle{c.handle}, onResume: func() {
		once.Do(launched.Done)
		<-release
	}}
	// Retire first, so the session is marked; the input then starts it and holds the read side
	// inside the launch.
	if code, _ := postRetireIf(t, "s", -1); code != http.StatusOK {
		t.Fatal("retire")
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- postTo(t, HandleSessionInput, "s", "input", `{"prompt":"go on"}`) }()
	launched.Wait()
	retired := make(chan struct{})
	go func() { postRetireIf(t, "s", -1); close(retired) }() // waits behind the start's read side
	select {
	case <-retired:
		t.Fatal("a retire went through a start that was still launching")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if rec := <-done; rec.Code != http.StatusOK {
		t.Fatalf("input = %d %s", rec.Code, rec.Body)
	}
	<-retired
	// The retire came after the start: it marks and stops again, so nothing is marked and running.
	cur, _ := session.ReadMeta("s")
	if cur.RetiredAt != "" && ManagedAlive(cur) {
		t.Fatal("marked and running")
	}
}

// gatedDriver is a driver whose Resume runs onResume first and then hands out h.
type gatedDriver struct {
	agents.Driver
	h        agents.ThreadHandle
	onResume func()
}

func (d *gatedDriver) Resume(session.Meta) (agents.ThreadHandle, error) {
	d.onResume()
	return d.h, nil
}

// plannerWithAutoReviewer is a retired, stopped session "s" whose plan review runs a reviewer
// "rev" on its own.
func (c *carriedEnv) plannerWithAutoReviewer(t *testing.T, k carriedKind, enabled bool) {
	t.Helper()
	session.WriteMeta(session.Meta{Name: "rev", Kind: session.KindClaude, Dir: t.TempDir()})
	session.WriteMeta(session.Meta{Name: "s", Kind: k.kind, Driver: k.driver, Dir: t.TempDir(),
		PlanReviewers: []session.PlanReviewer{{Reviewer: "rev", Auto: &session.PlanAuto{Enabled: enabled}}}})
	if code, out := postRetireIf(t, "s", -1); code != http.StatusOK || !reportOf(out, "s").Stopped {
		t.Fatalf("retire = %d %+v", code, out)
	}
}

// A peer message to a stopped session starts it first, and says who sends it. The start for an auto
// plan reviewer's reply is refused on a retired planner, so the later /input never gets to be
// judged on a session already started; another sender's start is deliberate.
func TestStartBeforeAPeerMessageKnowsTheSender(t *testing.T) {
	for _, k := range carriedKinds {
		t.Run(k.label, func(t *testing.T) {
			c := newCarriedEnv(t, "")
			c.plannerWithAutoReviewer(t, k, true)
			c.wantRefused(t, k, c.start(t, "?peer_from=rev"), "the reviewer's start")
			session.WriteMeta(session.Meta{Name: "other", Kind: session.KindClaude, Dir: t.TempDir()})
			if rec := c.start(t, "?peer_from=other"); rec.Code != http.StatusOK {
				t.Fatalf("another peer's start = %d %s", rec.Code, rec.Body)
			}
			c.wantStarted(t, k, "another peer's start")
		})
	}
}

// What makes a reply the automatic reviewer's does not change when the loop ends or the reply is
// sent again: a refused verdict sent a second time, and one sent after the loop is over, are
// refused the same way.
func TestAutoReviewerReplyStaysAutomaticAfterTheLoopEnds(t *testing.T) {
	for _, k := range carriedKinds {
		t.Run(k.label, func(t *testing.T) {
			c := newCarriedEnv(t, "")
			c.plannerWithAutoReviewer(t, k, true)
			verdict := `{"prompt":"## Verdict\napprove","peer_from":"rev","peer_intent":"answer"}`
			for i := 0; i < 2; i++ {
				c.wantRefusedKeepsMark(t, k, postTo(t, HandleSessionInput, "s", "input", verdict), "verdict")
			}
			if cur, _ := session.ReadMeta("s"); cur.RetiredAt == "" {
				t.Fatal("a resent verdict took the mark off")
			}
			c = newCarriedEnv(t, "")
			prevRate := peerRate
			peerRate = &peerLimiter{sends: map[string][]time.Time{}, recent: map[string]time.Time{}}
			t.Cleanup(func() { peerRate = prevRate })
			c.plannerWithAutoReviewer(t, k, false) // the loop is already over
			c.wantRefusedKeepsMark(t, k, postTo(t, HandleSessionInput, "s", "input", verdict), "a late verdict")
		})
	}
}

// The plan review's own follow-up to its reviewer is automatic whatever its body says: a reviewer
// retired between the readiness check and the send is not started by it.
func TestPlanFollowUpDoesNotStartARetiredReviewer(t *testing.T) {
	for _, k := range carriedKinds {
		t.Run(k.label, func(t *testing.T) {
			c := newCarriedEnv(t, "")
			session.WriteMeta(session.Meta{Name: "planner", Kind: session.KindClaude, Dir: t.TempDir()})
			c.retiredStopped(t, k) // "s" is the reviewer
			err := sendPlanFollowUp("planner", "s", "please look again")
			if err == nil || !(strings.Contains(err.Error(), "session_retired") || strings.Contains(err.Error(), "not_running")) {
				t.Fatalf("follow-up err = %v, want session_retired", err)
			}
			cur, _ := session.ReadMeta("s")
			if cur.RetiredAt == "" || cur.PersonActMark != 0 {
				t.Fatalf("retiredAt=%q actMark=%d", cur.RetiredAt, cur.PersonActMark)
			}
		})
	}
}
