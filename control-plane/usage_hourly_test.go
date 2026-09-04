package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// usageHourFixture: one tenant, one member, one workspace.
func usageHourFixture(t *testing.T) (*store.SQL, *manager, store.Tenant, store.Membership, store.Workspace) {
	t.Helper()
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	mgr.usageInterval = 5 * time.Minute
	tn, _ := st.CreateTenant(ctx, "sales", "営業部")
	id, _ := st.UpsertIdentity(ctx, "w@acme.co.jp", "w-acme-co-jp", "")
	mem, err := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	ws := store.Workspace{
		ID: "W-1", TenantID: tn.ID, MembershipID: mem.ID,
		ContainerName: "af-ws-sales-w", DataDir: "/srv/data/sales/w",
		AgentPort: "7731", AgentToken: "tok", State: "running", CreatedAt: store.NowTS(),
	}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	return st, mgr, tn, mem, ws
}

// TestAddUsageHourAddsSumsAndKeepsThePeak — totals add up, peaks do not. Making max_sessions
// a SUM stacks one session per five-minute sample into "12 running at once in that hour",
// a picture of something that never happened.
func TestAddUsageHourAddsSumsAndKeepsThePeak(t *testing.T) {
	ctx := context.Background()
	st, _, tn, mem, _ := usageHourFixture(t)
	for _, c := range []store.UsageHourCounters{
		{Samples: 1, RunningSecs: 300, MeasuredSecs: 300, SessionSecs: 600, BusySecs: 300, MaxSessions: 2, MaxBusy: 1},
		{Samples: 1, RunningSecs: 300, MeasuredSecs: 300, SessionSecs: 1200, BusySecs: 0, MaxSessions: 4, MaxBusy: 0},
		{Samples: 1, RunningSecs: 300, MeasuredSecs: 0, SessionSecs: 0, BusySecs: 0, MaxSessions: 0, MaxBusy: 0},
	} {
		if err := st.AddUsageHour(ctx, mem.ID, tn.ID, "2026-09-01T09", c); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	rows, err := st.ListUsageHourly(ctx, "", "2026-09-01T00", "2026-09-01T23")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	got := rows[0].UsageHourCounters
	want := store.UsageHourCounters{Samples: 3, RunningSecs: 900, MeasuredSecs: 600, SessionSecs: 1800, BusySecs: 300, MaxSessions: 4, MaxBusy: 1}
	if got != want {
		t.Errorf("counters = %+v, want %+v", got, want)
	}
	// Labels are joined at read time, so history outlives a deleted member — the same
	// practice as UsageRow.
	if rows[0].UserKey != "w-acme-co-jp" || rows[0].TenantSlug != "sales" {
		t.Errorf("labels not joined: %+v", rows[0])
	}
}

// TestListUsageHourlyKeepsTheHeartbeatUnderATenantFilter — heartbeat rows carry no tenant.
// Dropping them along with a tenant filter makes a tenant admin's heatmap read entirely
// "unobserved", i.e. every cell blank.
func TestListUsageHourlyKeepsTheHeartbeatUnderATenantFilter(t *testing.T) {
	ctx := context.Background()
	st, _, tn, mem, _ := usageHourFixture(t)
	other, _ := st.CreateTenant(ctx, "eng", "開発部")
	oid, _ := st.UpsertIdentity(ctx, "e@acme.co.jp", "e-acme-co-jp", "")
	omem, _ := st.EnsureMembership(ctx, oid.ID, other.ID, "member")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	must(st.AddUsageHour(ctx, "", "", "2026-09-01T09", store.UsageHourCounters{Samples: 12}))
	must(st.AddUsageHour(ctx, mem.ID, tn.ID, "2026-09-01T09", store.UsageHourCounters{Samples: 12, RunningSecs: 3600}))
	must(st.AddUsageHour(ctx, omem.ID, other.ID, "2026-09-01T09", store.UsageHourCounters{Samples: 12, RunningSecs: 3600}))

	rows, err := st.ListUsageHourly(ctx, tn.ID, "2026-09-01T00", "2026-09-01T23")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var heartbeats, members int
	for _, r := range rows {
		if r.MembershipID == "" {
			heartbeats++
			continue
		}
		members++
		if r.TenantSlug != "sales" {
			t.Errorf("another tenant's row leaked: %+v", r)
		}
	}
	if heartbeats != 1 {
		t.Errorf("heartbeat rows = %d, want 1 — without it every empty hour reads as unobserved", heartbeats)
	}
	if members != 1 {
		t.Errorf("member rows = %d, want just this tenant's 1", members)
	}
}

func TestPruneUsageHourly(t *testing.T) {
	ctx := context.Background()
	st, _, tn, mem, _ := usageHourFixture(t)
	for _, h := range []string{"2026-05-01T09", "2026-09-01T09"} {
		if err := st.AddUsageHour(ctx, mem.ID, tn.ID, h, store.UsageHourCounters{Samples: 1, RunningSecs: 300}); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	if err := st.PruneUsageHourly(ctx, "2026-08-01T00"); err != nil {
		t.Fatalf("prune: %v", err)
	}
	rows, _ := st.ListUsageHourly(ctx, "", "2026-01-01T00", "2026-12-31T23")
	if len(rows) != 1 || rows[0].Hour != "2026-09-01T09" {
		t.Fatalf("rows = %+v, want only the recent hour", rows)
	}
}

// --- the sampler -----------------------------------------------------------

// agentSessionsServer plays the Agent's GET /sessions.
func agentSessionsServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSampleRecordsHourlyOccupancyAndSessionCounts — one sweep writes both the daily and the
// hourly bucket, counting live sessions separately from "the machine is working".
//
// What is pinned here is the definition of those counts: alive is 3, but the only one that
// must not be stopped is the 1 working session (a question waiting on a human, and dead
// rows, do not count).
func TestSampleRecordsHourlyOccupancyAndSessionCounts(t *testing.T) {
	ctx := context.Background()
	st, mgr, _, mem, _ := usageHourFixture(t)
	srv := agentSessionsServer(t, `{"sessions":[
		{"name":"a","alive":true,"state":"working"},
		{"name":"b","alive":true,"state":"question"},
		{"name":"c","alive":true,"state":"idle"},
		{"name":"d","alive":false,"state":"working"}
	]}`)
	mgr.rtFactory = stubFactory{rt: stubRuntime{endpoint: srv.URL}}

	u := newUsageSampler(mgr, 5*time.Minute)
	u.sample(ctx)

	rows, err := st.ListUsageHourly(ctx, "", "2000-01-01T00", "2999-01-01T00")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var member, beat *store.UsageHourRow
	for i := range rows {
		if rows[i].MembershipID == mem.ID {
			member = &rows[i]
		} else if rows[i].MembershipID == "" {
			beat = &rows[i]
		}
	}
	if member == nil {
		t.Fatal("no hourly row for the running workspace")
	}
	if beat == nil {
		t.Fatal("no heartbeat row — an hour with no member row would read as unobserved rather than stopped")
	}
	want := store.UsageHourCounters{
		Samples: 1, RunningSecs: 300, MeasuredSecs: 300,
		SessionSecs: 3 * 300, BusySecs: 1 * 300, MaxSessions: 3, MaxBusy: 1,
	}
	if member.UsageHourCounters != want {
		t.Errorf("counters = %+v, want %+v", member.UsageHourCounters, want)
	}
	// The daily bucket still advances: the hourly bucket is an addition, not a replacement.
	days, _ := st.ListUsage(ctx, "", "2000-01-01", "2999-01-01")
	if len(days) != 1 || days[0].RunningSecs != 300 {
		t.Errorf("daily showback = %+v, want the same 300s it always recorded", days)
	}
}

// TestSampleLeavesSessionsUnmeasuredWhenTheAgentIsUnreachable — not reaching the Agent is
// not "zero sessions". A booting or wedged Agent is precisely the unknown case, and
// recording 0 paints a cold cell over an hour that was busy.
func TestSampleLeavesSessionsUnmeasuredWhenTheAgentIsUnreachable(t *testing.T) {
	ctx := context.Background()
	st, mgr, _, mem, _ := usageHourFixture(t)
	// An address nobody listens on. State stays running: the container does exist.
	mgr.rtFactory = stubFactory{rt: stubRuntime{endpoint: "http://127.0.0.1:1"}}

	newUsageSampler(mgr, 5*time.Minute).sample(ctx)

	rows, _ := st.ListUsageHourly(ctx, "", "2000-01-01T00", "2999-01-01T00")
	var member *store.UsageHourRow
	for i := range rows {
		if rows[i].MembershipID == mem.ID {
			member = &rows[i]
		}
	}
	if member == nil {
		t.Fatal("a running workspace with an unreachable Agent still occupies the host")
	}
	if member.RunningSecs != 300 {
		t.Errorf("running_secs = %d, want 300", member.RunningSecs)
	}
	if member.MeasuredSecs != 0 || member.SessionSecs != 0 || member.MaxSessions != 0 {
		t.Errorf("counted sessions we never read: %+v", member.UsageHourCounters)
	}
}

// TestSampleWritesNoMemberRowForAStoppedWorkspace — a stopped workspace writes no row; grey
// comes out of the difference against the heartbeat.
func TestSampleWritesNoMemberRowForAStoppedWorkspace(t *testing.T) {
	ctx := context.Background()
	st, mgr, _, mem, _ := usageHourFixture(t)
	mgr.rtFactory = stubFactory{rt: stubRuntime{state: "stopped"}}

	newUsageSampler(mgr, 5*time.Minute).sample(ctx)

	rows, _ := st.ListUsageHourly(ctx, "", "2000-01-01T00", "2999-01-01T00")
	if len(rows) != 1 || rows[0].MembershipID != "" {
		t.Fatalf("rows = %+v, want only the heartbeat", rows)
	}
	_ = mem
}

// --- assembling the response -----------------------------------------------

func TestBuildUsageHourlySeparatesHeartbeatsAndDropsSystemTenants(t *testing.T) {
	got := buildUsageHourly([]store.UsageHourRow{
		{MembershipID: "", Hour: "2026-09-01T09", UsageHourCounters: store.UsageHourCounters{Samples: 12}},
		{MembershipID: "m1", TenantSlug: "sales", UserKey: "w", Hour: "2026-09-01T09",
			UsageHourCounters: store.UsageHourCounters{Samples: 12, RunningSecs: 3600}},
		{MembershipID: "m1", TenantSlug: "sales", UserKey: "w", Hour: "2026-09-01T10",
			UsageHourCounters: store.UsageHourCounters{Samples: 6, RunningSecs: 1800}},
		{MembershipID: "m2", TenantSlug: systemTenantSlugs()[0], UserKey: "", Hour: "2026-09-01T09",
			UsageHourCounters: store.UsageHourCounters{Samples: 12, RunningSecs: 3600}},
	}, "2026-09-01", "2026-09-01", 300)

	// samples rides along with the hour, not just the timestamp. It is a cell's denominator
	// (the seconds actually watched); without it a client can only assume 3600s per hour,
	// and the current, still-unfinished hour fades exactly like an hour the CP was down.
	if len(got.Observed) != 1 || got.Observed[0].Hour != "2026-09-01T09" || got.Observed[0].Samples != 12 {
		t.Errorf("observed = %+v, want the heartbeat hour with its sample count", got.Observed)
	}
	if len(got.Members) != 1 {
		t.Fatalf("members = %+v, want only the real one (heartbeat is not a member, system tenants are not people)", got.Members)
	}
	if got.Members[0].UserKey != "w" || len(got.Members[0].Hours) != 2 {
		t.Errorf("member = %+v", got.Members[0])
	}
	if got.IntervalSecs != 300 {
		t.Errorf("interval_secs = %d — the client divides by it", got.IntervalSecs)
	}
}

func TestUsageHourWindowDefaultsToFourteenDays(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	from, to, fromHour, toHour, aerr := usageHourWindow(httptest.NewRequest(http.MethodGet, "/x", nil), now)
	if aerr != nil {
		t.Fatalf("err: %+v", aerr)
	}
	if from != "2026-08-19" || to != "2026-09-01" {
		t.Errorf("window = %s..%s, want a trailing 14 days", from, to)
	}
	if fromHour != "2026-08-19T00" || toHour != "2026-09-01T23" {
		t.Errorf("hours = %s..%s, want whole days", fromHour, toHour)
	}
}

func TestUsageHourWindowRejectsGarbageRatherThanDefaulting(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for _, q := range []string{"?from=yesterday", "?to=2026-13-40", "?from=2026-09-02&to=2026-09-01"} {
		if _, _, _, _, aerr := usageHourWindow(httptest.NewRequest(http.MethodGet, "/x"+q, nil), now); aerr == nil {
			t.Errorf("%s was accepted; a mistyped range must be a 400, not a silent default", q)
		}
	}
}

// TestMyUsageHourlyIsScopedToTheCaller — a personal surface returns only the caller's own,
// and no value from which someone else's total could be recovered by subtraction (the same
// practice as /api/cost/me). The heartbeat belongs to nobody, so it stays.
func TestMyUsageHourlyIsScopedToTheCaller(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, mem, _ := usageHourFixture(t)
	other, _ := st.CreateTenant(ctx, "eng", "開発部")
	oid, _ := st.UpsertIdentity(ctx, "e@acme.co.jp", "e-acme-co-jp", "")
	omem, _ := st.EnsureMembership(ctx, oid.ID, other.ID, "member")
	_ = st.AddUsageHour(ctx, "", "", "2026-09-01T09", store.UsageHourCounters{Samples: 12})
	_ = st.AddUsageHour(ctx, mem.ID, tn.ID, "2026-09-01T09", store.UsageHourCounters{Samples: 12, RunningSecs: 3600})
	_ = st.AddUsageHour(ctx, omem.ID, other.ID, "2026-09-01T09", store.UsageHourCounters{Samples: 12, RunningSecs: 3600})

	r := httptest.NewRequest(http.MethodGet, "/api/usage/me/hourly?from=2026-09-01&to=2026-09-01", nil)
	w := httptest.NewRecorder()
	newAdminAPI(mgr).myUsageHourly(w, r, store.Identity{}, store.MembershipView{MembershipID: mem.ID, TenantID: tn.ID, TenantSlug: tn.Slug})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got usageHourlyResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Members) != 1 || got.Members[0].UserKey != "w-acme-co-jp" {
		t.Fatalf("members = %+v, want only the caller", got.Members)
	}
	if len(got.Observed) != 1 {
		t.Errorf("observed = %v — the caller still needs to know which hours were watched", got.Observed)
	}
}
