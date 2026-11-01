package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestLivenessClockStartsAtFirstSightAndNeverAnsweredGetsDouble(t *testing.T) {
	l := &agentLiveness{m: map[string]*liveEntry{}}
	t0 := time.Unix(1_000_000, 0)
	after := 3 * time.Minute
	// First sight starts the clock: a fresh CP (or a fresh workspace) cannot alarm at once.
	if s, p, _ := l.assess("a", t0, after); s || p {
		t.Fatal("first sight must neither flag nor probe")
	}
	// Never answered: the limit is twice `after`.
	if _, p, _ := l.assess("a", t0.Add(after+time.Second), after); p {
		t.Fatal("probed before 2×after for an Agent that never answered")
	}
	if _, p, _ := l.assess("a", t0.Add(2*after+time.Second), after); !p {
		t.Fatal("no probe after 2×after of silence")
	}
	// A second assess while the probe is in flight does not start another.
	if _, p, _ := l.assess("a", t0.Add(2*after+2*time.Second), after); p {
		t.Fatal("probe started twice")
	}
	if !l.probeDone("a", false) {
		t.Fatal("a failed probe must flip to silent")
	}
	if s, _, _ := l.assess("a", t0.Add(2*after+3*time.Second), after); !s {
		t.Fatal("not silent after a failed probe")
	}
	// An answer clears it and is reported once as a recovery.
	l.note("a", t0.Add(3*after))
	if s, _, rec := l.assess("a", t0.Add(3*after), after); s || !rec {
		t.Fatalf("after an answer: silent=%v recovered=%v", s, rec)
	}
	if _, _, rec := l.assess("a", t0.Add(3*after), after); rec {
		t.Fatal("recovery reported twice")
	}
	// Once it has answered, the plain period applies.
	if _, p, _ := l.assess("a", t0.Add(3*after+after+time.Second), after); !p {
		t.Fatal("no probe after `after` of silence following an answer")
	}
}

func TestLivenessAnsweredProbeStaysQuietAndForgetRestartsTheClock(t *testing.T) {
	l := &agentLiveness{m: map[string]*liveEntry{}}
	t0 := time.Unix(1_000_000, 0)
	l.assess("a", t0, time.Minute)
	l.note("a", t0)
	if _, p, _ := l.assess("a", t0.Add(61*time.Second), time.Minute); !p {
		t.Fatal("expected a probe")
	}
	if l.probeDone("a", true) {
		t.Fatal("an answered probe must not flip")
	}
	l.forget("a")
	if s, p, _ := l.assess("a", t0.Add(time.Hour), time.Minute); s || p {
		t.Fatal("after forget the next sight starts a new clock")
	}
	// note on an unknown key must not create an entry (no unbounded growth).
	l.note("zzz", t0)
	if _, ok := l.m["zzz"]; ok {
		t.Fatal("note created an entry")
	}
}

func TestAnswerTransportCountsAgentAnswersButNotGatewayErrors(t *testing.T) {
	var code atomic.Int32
	code.Store(200)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(int(code.Load())) }))
	defer srv.Close()
	key := endpointKey(srv.URL)
	agentLive.forget(key)
	defer agentLive.forget(key)
	agentLive.assess(key, time.Now().Add(-time.Hour), time.Minute) // entry exists, never answered
	c := &http.Client{Transport: newAnswerTransport()}
	for _, st := range []int{502, 503, 504} {
		code.Store(int32(st))
		resp, err := c.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if agentLive.answeredWithin(key, time.Now(), time.Hour) {
			t.Fatalf("a %d counted as an Agent answer", st)
		}
	}
	// An Agent's own 500 is still an answer: it is alive and handling requests.
	code.Store(500)
	resp, _ := c.Get(srv.URL)
	resp.Body.Close()
	if !agentLive.answeredWithin(key, time.Now(), time.Hour) {
		t.Fatal("a 500 from the Agent was not counted")
	}
}

func waitLive(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestDeadManFlagsASilentAgentOnceAndClearsOnAnswer drives the whole path: a workspace that
// answers is never flagged, one whose Agent stops is flagged after the period, the tenant
// admin is told exactly once however often the check runs, and an answer retires the flag.
func TestDeadManFlagsASilentAgentOnceAndClearsOnAnswer(t *testing.T) {
	t.Setenv("AF_AGENT_SILENT_AFTER", "100ms")
	old := agentSilentProbeEvery
	agentSilentProbeEvery = 10 * time.Millisecond
	defer func() { agentSilentProbeEvery = old }()
	f := newNoticeFixture(t, false)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	rt := stubRuntime{endpoint: srv.URL}
	key := endpointKey(srv.URL)
	defer agentLive.forget(key)
	check := func(state string) bool { return f.mgr.checkAgentSilent(ctx, f.ws, rt, state) }

	if check("running") {
		t.Fatal("flagged on first sight")
	}
	// Alive but nobody has talked to it for longer than the period: the probe finds it
	// answering, so there is no flag (a replica that merely missed the traffic stays quiet).
	time.Sleep(450 * time.Millisecond) // never-answered limit is 2×
	check("running")
	waitLive(t, "an answered probe", func() bool { return agentLive.answeredWithin(key, time.Now(), time.Minute) })
	if check("running") {
		t.Fatal("an Agent that answers the probe was flagged")
	}

	// The Agent goes dark (connection refused; the process-wedged case is the same to the probe).
	srv.Close()
	time.Sleep(150 * time.Millisecond)
	check("running") // starts the probe
	waitLive(t, "the silent flag", func() bool { return check("running") })
	waitLive(t, "the admin notice", func() bool { return f.kinds(t, f.admin, "member-agent-silent") == 1 })
	for i := 0; i < 3; i++ {
		check("running")
	}
	time.Sleep(50 * time.Millisecond)
	if n := f.kinds(t, f.admin, "member-agent-silent"); n != 1 {
		t.Fatalf("admin notified %d times for one outage", n)
	}
	if n := f.kinds(t, f.member, "member-agent-silent"); n != 0 {
		t.Fatalf("a plain member got the admin notice (%d)", n)
	}

	// The member's workspace payload carries the flag, only while running.
	api := newWorkspaceAPI(f.mgr, false)
	res := &resolved{rt: rt, ws: f.ws}
	if p := api.workspacePayload(ctx, res, "running"); p["agentSilent"] != true {
		t.Fatalf("payload lacks agentSilent: %v", p)
	}

	// Recovery: an answer retires the flag and the notice mark.
	raised := func() bool {
		r, _ := f.st.WorkspaceNoticeRaised(ctx, f.ws.ID, store.WorkspaceNoticeAgentSilent)
		return r
	}
	if !raised() {
		t.Fatal("the notice mark is not set")
	}
	agentLive.note(key, time.Now())
	if check("running") {
		t.Fatal("still flagged after an answer")
	}
	if raised() {
		t.Fatal("the notice mark survived the recovery")
	}

	// Never flagged while the runtime says starting or stopping.
	if check("starting") || check("stopped") {
		t.Fatal("flagged outside running")
	}
	if p := api.workspacePayload(ctx, res, "starting"); p["agentSilent"] != nil {
		t.Fatalf("payload flags a starting workspace: %v", p)
	}
}
