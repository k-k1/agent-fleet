package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/adminnotice"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestLivenessClockStartsAtFirstSightAndNeverAnsweredGetsDouble(t *testing.T) {
	l := &agentLiveness{m: map[string]*liveEntry{}}
	t0 := time.Unix(1_000_000, 0)
	after := 3 * time.Minute
	// First sight starts the clock: a fresh CP (or a fresh workspace) cannot alarm at once.
	if s, p, _ := l.assess("a", t0, after); s || p != nil {
		t.Fatal("first sight must neither flag nor probe")
	}
	// Never answered: the limit is twice `after`.
	if _, p, _ := l.assess("a", t0.Add(after+time.Second), after); p != nil {
		t.Fatal("probed before 2×after for an Agent that never answered")
	}
	_, tok, _ := l.assess("a", t0.Add(2*after+time.Second), after)
	if tok == nil {
		t.Fatal("no probe after 2×after of silence")
	}
	// A second assess while the probe is in flight does not start another.
	if _, p, _ := l.assess("a", t0.Add(2*after+2*time.Second), after); p != nil {
		t.Fatal("probe started twice")
	}
	if !l.probeDone("a", *tok, false) {
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
	if _, p, _ := l.assess("a", t0.Add(3*after+after+time.Second), after); p == nil {
		t.Fatal("no probe after `after` of silence following an answer")
	}
}

func TestLivenessAnsweredProbeStaysQuietAndForgetRestartsTheClock(t *testing.T) {
	l := &agentLiveness{m: map[string]*liveEntry{}}
	t0 := time.Unix(1_000_000, 0)
	l.assess("a", t0, time.Minute)
	l.note("a", t0)
	_, tok, _ := l.assess("a", t0.Add(61*time.Second), time.Minute)
	if tok == nil {
		t.Fatal("expected a probe")
	}
	if l.probeDone("a", *tok, true) {
		t.Fatal("an answered probe must not flip")
	}
	l.forget("a")
	if s, p, _ := l.assess("a", t0.Add(time.Hour), time.Minute); s || p != nil {
		t.Fatal("after forget the next sight starts a new clock")
	}
	// note on an unknown key must not create an entry (no unbounded growth).
	l.note("zzz", t0)
	if _, ok := l.m["zzz"]; ok {
		t.Fatal("note created an entry")
	}
}

// A probe that failed is stale evidence when an answer arrived after it started, or when the
// workspace was stopped and started again under it.
func TestLivenessDropsAFailedProbeThatANewerAnswerOrGenerationOutdates(t *testing.T) {
	l := &agentLiveness{m: map[string]*liveEntry{}}
	t0 := time.Unix(1_000_000, 0)
	l.assess("a", t0, time.Minute)
	l.note("a", t0)
	_, tok, _ := l.assess("a", t0.Add(61*time.Second), time.Minute)
	l.note("a", t0.Add(62*time.Second)) // some other request succeeded meanwhile
	if l.probeDone("a", *tok, false) {
		t.Fatal("a probe that failed before a later answer flipped the entry")
	}
	if s, _, _ := l.assess("a", t0.Add(63*time.Second), time.Minute); s {
		t.Fatal("flagged on stale evidence")
	}

	l.note("a", t0)
	_, tok, _ = l.assess("a", t0.Add(130*time.Second), time.Minute)
	if tok == nil {
		t.Fatal("expected a probe")
	}
	l.forget("a")
	l.assess("a", t0.Add(131*time.Second), time.Minute) // a new running spell
	if l.probeDone("a", *tok, false) {
		t.Fatal("a probe from the previous spell flagged the new one")
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

func countAuditAction(t *testing.T, st *store.SQL, tenantID, action string) int {
	t.Helper()
	rows, err := st.ListAuditByTenant(context.Background(), tenantID, 100)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rows {
		if r.Action == action {
			n++
		}
	}
	return n
}

// Two replicas that both see one outage write one audit row for it, and one for the recovery:
// the rows follow the shared mark's real transitions, not each replica's own view.
func TestAgentSilentAuditFollowsTheSharedMarkNotEachReplica(t *testing.T) {
	f := newNoticeFixture(t, false)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		adminnotice.AgentSilent(ctx, f.st, f.ws, true)
	}
	if n := countAuditAction(t, f.st, f.ws.TenantID, "workspace.agent_silent"); n != 1 {
		t.Fatalf("silent audit rows = %d, want 1", n)
	}
	for i := 0; i < 2; i++ {
		adminnotice.AgentSilent(ctx, f.st, f.ws, false)
	}
	if n := countAuditAction(t, f.st, f.ws.TenantID, "workspace.agent_recovered"); n != 1 {
		t.Fatalf("recovered audit rows = %d, want 1", n)
	}
	// Retiring a mark nobody raised writes nothing.
	adminnotice.AgentSilent(ctx, f.st, f.ws, false)
	if n := countAuditAction(t, f.st, f.ws.TenantID, "workspace.agent_recovered"); n != 1 {
		t.Fatalf("recovered audit rows after a no-op = %d, want 1", n)
	}
}

// A mark left by a replica that died mid-outage is retired by a healthy, unwatched workspace's
// own sweep, using the answer that sweep just got.
func TestSweepRetiresAMarkLeftByAnotherReplicaOnceTheAgentAnswers(t *testing.T) {
	f := newNoticeFixture(t, false)
	ctx := context.Background()
	key := endpointKey(f.mgr.runtimeFor(f.ws, noSecretKeys).Endpoint())
	agentLive.forget(key)
	defer agentLive.forget(key)
	adminnotice.AgentSilent(ctx, f.st, f.ws, true)
	if r, _ := f.st.WorkspaceNoticeRaised(ctx, f.ws.ID, store.WorkspaceNoticeAgentSilent); !r {
		t.Fatal("setup: no mark")
	}
	f.sample() // first sight on this replica; the Agent answers the sweep's own calls
	if r, _ := f.st.WorkspaceNoticeRaised(ctx, f.ws.ID, store.WorkspaceNoticeAgentSilent); r {
		t.Fatal("the mark survived a sweep in which the Agent answered")
	}
}

// Stopping and starting a workspace between two sweeps must reset the check through the
// handlers a Console actually polls, so the next running spell gets a fresh clock.
func TestPayloadAndMemberStatsResetTheClockOnNonRunningStates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	key := endpointKey(srv.URL)
	defer agentLive.forget(key)
	seed := func() {
		now := time.Now()
		agentLive.mu.Lock()
		agentLive.m[key] = &liveEntry{since: now.Add(-time.Hour), answered: now.Add(-time.Hour), probedAt: now, silent: true, gen: 1}
		agentLive.mu.Unlock()
	}
	_, mgr, _, _ := destroyFixture(t, stateOnlyFactory{state: "running"})
	api := newWorkspaceAPI(mgr, false)
	ctx := context.Background()
	res := func(state string) *resolved {
		return &resolved{rt: stateOnlyRuntime{stubRuntime: stubRuntime{endpoint: srv.URL}, state: state}}
	}
	seed()
	if p := api.workspacePayload(ctx, res("running"), "running"); p["agentSilent"] != true {
		t.Fatalf("setup: %v", p)
	}
	for _, st := range []string{"stopped", "starting"} {
		if p := api.workspacePayload(ctx, res(st), st); p["agentSilent"] != nil {
			t.Fatalf("%s payload flagged: %v", st, p)
		}
	}
	if p := api.workspacePayload(ctx, res("running"), "running"); p["agentSilent"] != nil {
		t.Fatalf("a new running spell inherited the old verdict: %v", p)
	}

	// The administrator's stats read does the same.
	for _, c := range []struct{ state string }{{"stopped"}} {
		_, mgr2, _, _ := destroyFixture(t, endpointFactory{state: c.state, endpoint: srv.URL})
		seed()
		r := httptest.NewRequest(http.MethodGet, "/api/admin/tenants/sales/members/leaver-acme-co-jp/stats", nil)
		r.SetPathValue("slug", "sales")
		r.SetPathValue("key", "leaver-acme-co-jp")
		r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
		newAdminAPI(mgr2).memberStats(httptest.NewRecorder(), r)
		agentLive.mu.Lock()
		_, still := agentLive.m[key]
		agentLive.mu.Unlock()
		if still {
			t.Fatal("memberStats on a stopped workspace left the entry")
		}
	}
}
