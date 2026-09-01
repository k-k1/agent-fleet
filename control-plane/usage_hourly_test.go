package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// usageHourFixture: 1 テナント・1 メンバー・1 ワークスペース。
func usageHourFixture(t *testing.T) (*sqlStore, *manager, Tenant, Membership, Workspace) {
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
	ws := Workspace{
		ID: "W-1", TenantID: tn.ID, MembershipID: mem.ID,
		ContainerName: "af-ws-sales-w", DataDir: "/srv/data/sales/w",
		AgentPort: "7731", AgentToken: "tok", State: "running", CreatedAt: nowTS(),
	}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	return st, mgr, tn, mem, ws
}

// 合計は足し、ピークは足さない。max_sessions を SUM にしてしまうと 5 分ごとに 1 本ずつ
// 積み上がって「1 時間に 12 本同時起動していた」という、実際には起きていない絵になる。
func TestAddUsageHourAddsSumsAndKeepsThePeak(t *testing.T) {
	ctx := context.Background()
	st, _, tn, mem, _ := usageHourFixture(t)
	for _, c := range []UsageHourCounters{
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
	want := UsageHourCounters{Samples: 3, RunningSecs: 900, MeasuredSecs: 600, SessionSecs: 1800, BusySecs: 300, MaxSessions: 4, MaxBusy: 1}
	if got != want {
		t.Errorf("counters = %+v, want %+v", got, want)
	}
	// ラベルは読むときに join する（メンバーが消えても履歴は残る、という UsageRow と同じ作法）。
	if rows[0].UserKey != "w-acme-co-jp" || rows[0].TenantSlug != "sales" {
		t.Errorf("labels not joined: %+v", rows[0])
	}
}

// ⚠️ ハートビート行はテナントを持たない。テナントで絞ったときにこれを一緒に落とすと、
// テナント管理者のヒートマップが**全部「未観測」**になる（＝全マス空白）。
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
	must(st.AddUsageHour(ctx, "", "", "2026-09-01T09", UsageHourCounters{Samples: 12}))
	must(st.AddUsageHour(ctx, mem.ID, tn.ID, "2026-09-01T09", UsageHourCounters{Samples: 12, RunningSecs: 3600}))
	must(st.AddUsageHour(ctx, omem.ID, other.ID, "2026-09-01T09", UsageHourCounters{Samples: 12, RunningSecs: 3600}))

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
		if err := st.AddUsageHour(ctx, mem.ID, tn.ID, h, UsageHourCounters{Samples: 1, RunningSecs: 300}); err != nil {
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

// --- サンプラ --------------------------------------------------------------

// agentSessionsServer は Agent の GET /sessions を演じる。
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

// 1 スイープが日バケットと時バケットの両方を書き、生きている本数と「機械が動いている」
// 本数を分けて数える。
//
// ここで固定したいのは本数の定義: alive は 3 本だが、止めてはならないのは working の
// 1 本だけ（human待ちの question と、死んでいる行は数えない）。
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
	var member, beat *UsageHourRow
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
	want := UsageHourCounters{
		Samples: 1, RunningSecs: 300, MeasuredSecs: 300,
		SessionSecs: 3 * 300, BusySecs: 1 * 300, MaxSessions: 3, MaxBusy: 1,
	}
	if member.UsageHourCounters != want {
		t.Errorf("counters = %+v, want %+v", member.UsageHourCounters, want)
	}
	// 日バケットも従来どおり進む（時バケットは置き換えではなく追加）。
	days, _ := st.ListUsage(ctx, "", "2000-01-01", "2999-01-01")
	if len(days) != 1 || days[0].RunningSecs != 300 {
		t.Errorf("daily showback = %+v, want the same 300s it always recorded", days)
	}
}

// ⚠️ Agent に届かない = 「セッション 0 本」ではない。起動途中や刺さった Agent はまさに
// 「分からない」場合で、0 と記録すると忙しかった時間に冷たいマスを描くことになる。
func TestSampleLeavesSessionsUnmeasuredWhenTheAgentIsUnreachable(t *testing.T) {
	ctx := context.Background()
	st, mgr, _, mem, _ := usageHourFixture(t)
	// 誰も listen していないアドレス。State は running（コンテナは在る）。
	mgr.rtFactory = stubFactory{rt: stubRuntime{endpoint: "http://127.0.0.1:1"}}

	newUsageSampler(mgr, 5*time.Minute).sample(ctx)

	rows, _ := st.ListUsageHourly(ctx, "", "2000-01-01T00", "2999-01-01T00")
	var member *UsageHourRow
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

// 止まっているワークスペースは行を書かない（灰色はハートビートとの差分で出す）。
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

// --- 応答の組み立て --------------------------------------------------------

func TestBuildUsageHourlySeparatesHeartbeatsAndDropsSystemTenants(t *testing.T) {
	got := buildUsageHourly([]UsageHourRow{
		{MembershipID: "", Hour: "2026-09-01T09", UsageHourCounters: UsageHourCounters{Samples: 12}},
		{MembershipID: "m1", TenantSlug: "sales", UserKey: "w", Hour: "2026-09-01T09",
			UsageHourCounters: UsageHourCounters{Samples: 12, RunningSecs: 3600}},
		{MembershipID: "m1", TenantSlug: "sales", UserKey: "w", Hour: "2026-09-01T10",
			UsageHourCounters: UsageHourCounters{Samples: 6, RunningSecs: 1800}},
		{MembershipID: "m2", TenantSlug: systemTenantSlugs()[0], UserKey: "", Hour: "2026-09-01T09",
			UsageHourCounters: UsageHourCounters{Samples: 12, RunningSecs: 3600}},
	}, "2026-09-01", "2026-09-01", 300)

	// ⚠️ 時刻だけでなく samples も載る。これがマスの分母（見ていた秒数）で、無いと
	// クライアントは 1 時間を 3600 秒と決め打つしかなくなり、まだ途中の「今の時間」と
	// CP が落ちていた時間が同じだけ薄くなる。
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

// 本人向けの面は本人のぶんだけ返す。他人の合計を引き算で復元できる値を出さない
// （/api/cost/me と同じ作法）。ハートビートは誰の物でもないので残る。
func TestMyUsageHourlyIsScopedToTheCaller(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, mem, _ := usageHourFixture(t)
	other, _ := st.CreateTenant(ctx, "eng", "開発部")
	oid, _ := st.UpsertIdentity(ctx, "e@acme.co.jp", "e-acme-co-jp", "")
	omem, _ := st.EnsureMembership(ctx, oid.ID, other.ID, "member")
	_ = st.AddUsageHour(ctx, "", "", "2026-09-01T09", UsageHourCounters{Samples: 12})
	_ = st.AddUsageHour(ctx, mem.ID, tn.ID, "2026-09-01T09", UsageHourCounters{Samples: 12, RunningSecs: 3600})
	_ = st.AddUsageHour(ctx, omem.ID, other.ID, "2026-09-01T09", UsageHourCounters{Samples: 12, RunningSecs: 3600})

	r := httptest.NewRequest(http.MethodGet, "/api/usage/me/hourly?from=2026-09-01&to=2026-09-01", nil)
	w := httptest.NewRecorder()
	newAdminAPI(mgr).myUsageHourly(w, r, Identity{}, MembershipView{MembershipID: mem.ID, TenantID: tn.ID, TenantSlug: tn.Slug})
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
