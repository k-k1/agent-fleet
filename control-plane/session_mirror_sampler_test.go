package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// One usage sample refreshes the session mirror of a running workspace nobody has opened,
// and keeps the fields the list handlers also write (Carried, Studio): ReplaceSessions swaps
// the whole set, so a writer that leaves them out erases them.
func TestSampleRefreshesTheSessionMirror(t *testing.T) {
	ctx := context.Background()
	st, mgr, _, _, ws := usageHourFixture(t)
	srv := agentSessionsServer(t, `{"sessions":[
		{"name":"a","alive":true,"state":"working","kind":"claude","carried":"question","studio":"st1"},
		{"name":"b","alive":false,"kind":"claude"}
	]}`)
	mgr.rtFactory = stubFactory{rt: stubRuntime{endpoint: srv.URL}}

	newUsageSampler(mgr, 5*time.Minute).sample(ctx)

	rows, err := st.ListSessions(ctx, ws.ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("mirror = %+v, %v; want 2 rows", rows, err)
	}
	byName := map[string]store.SessionRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	a := byName["a"]
	if a.State != "running" || a.Carried != "question" || a.Studio != "st1" || a.LastSeen == "" {
		t.Errorf("row a = %+v, want running with carried, studio and last_seen", a)
	}
	if byName["b"].State != "stopped" {
		t.Errorf("row b = %+v, want stopped", byName["b"])
	}
}

// An unreachable Agent is not "no sessions": the mirror is left as it was.
func TestSampleLeavesTheSessionMirrorWhenTheAgentIsUnreachable(t *testing.T) {
	ctx := context.Background()
	st, mgr, _, _, ws := usageHourFixture(t)
	if err := st.ReplaceSessions(ctx, ws.ID, []store.SessionRow{{Name: "keep", Kind: "claude", State: "running"}}); err != nil {
		t.Fatal(err)
	}
	mgr.rtFactory = stubFactory{rt: stubRuntime{endpoint: "http://127.0.0.1:1"}}

	newUsageSampler(mgr, 5*time.Minute).sample(ctx)

	rows, _ := st.ListSessions(ctx, ws.ID)
	if len(rows) != 1 || rows[0].Name != "keep" {
		t.Errorf("mirror = %+v, want the earlier row untouched", rows)
	}
}

// The roster answers sessions and last_active_at from the DB alone: no Agent call per member.
func TestAdminMembersCarrySessionsAndLastActiveFromTheDB(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, mv, _ := networkFixture(t)
	ws := store.Workspace{ID: store.NewID(), TenantID: tn.ID, MembershipID: mv.MembershipID,
		ContainerName: "af-yamada", Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t",
		State: "running", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	last := setLastActive(t, st, ws.ID, time.Now().Add(-3*time.Hour))
	if err := st.ReplaceSessions(ctx, ws.ID, []store.SessionRow{
		{Name: "a", Kind: "claude", State: "running"},
		{Name: "b", Kind: "claude", State: "running"},
		{Name: "c", Kind: "claude", State: "stopped"},
	}); err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	mgr.rtFactory = stubFactory{rt: stubRuntime{endpoint: srv.URL}}

	row := memberRows(t, mgr, "boss@acme.co.jp")["yamada-acme-co-jp"]
	sess, _ := row["sessions"].(map[string]any)
	if sess == nil || sess["live"] != float64(2) || sess["total"] != float64(3) || sess["observed_at"] == nil {
		t.Errorf("sessions = %v, want live 2 / total 3 with observed_at", row["sessions"])
	}
	if row["last_active_at"] != last {
		t.Errorf("last_active_at = %v, want %s", row["last_active_at"], last)
	}
	if hits.Load() != 0 {
		t.Errorf("the roster called an Agent %d times", hits.Load())
	}

	// Stopped: the mirrored "running" rows are not alive any more.
	mgr.rtFactory = stubFactory{rt: stubRuntime{endpoint: srv.URL, state: "stopped"}}
	sess, _ = memberRows(t, mgr, "boss@acme.co.jp")["yamada-acme-co-jp"]["sessions"].(map[string]any)
	if sess["live"] != float64(0) || sess["total"] != float64(3) {
		t.Errorf("stopped sessions = %v, want live 0 / total 3", sess)
	}
}

// A slow read A (started first) that answers after a newer read B was written must not roll
// the mirror back to the older list, and must not stamp it "fresh" either.
func TestSampleDoesNotOverwriteANewerMirrorWriteWithASlowOlderRead(t *testing.T) {
	ctx := context.Background()
	st, mgr, _, _, ws := usageHourFixture(t)
	arrived := make(chan struct{})
	release := make(chan struct{})
	var arrivedOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrivedOnce.Do(func() { close(arrived) })
		<-release
		_, _ = w.Write([]byte(`{"sessions":[{"name":"old","alive":true,"kind":"claude"}]}`))
	}))
	defer srv.Close()
	mgr.rtFactory = stubFactory{rt: stubRuntime{endpoint: srv.URL}}

	done := make(chan struct{})
	go func() {
		newUsageSampler(mgr, 5*time.Minute).sample(ctx)
		close(done)
	}()
	<-arrived // A is in flight, its read already started

	// B: a list handler reads and writes after A started.
	if err := mgr.mirrorSessions(ctx, ws.ID, time.Now(), []sessionWire{{Name: "new", Kind: "claude", Carried: "question"}}); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done

	rows, _ := st.ListSessions(ctx, ws.ID)
	if len(rows) != 1 || rows[0].Name != "new" || rows[0].Carried != "question" {
		t.Errorf("mirror = %+v, want only the newer row \"new\"", rows)
	}
}

// Both admin session lists carry origin and scheduleId (and originSession on the member list)
// from the DB mirror, and the overview row copies them from live and mirrored rows alike; an
// empty value is left out of the JSON.
func TestAdminSessionListsCarryOriginAndScheduleID(t *testing.T) {
	ctx := context.Background()
	st, mgr, _, _, ws := usageHourFixture(t)
	if err := st.ReplaceSessions(ctx, ws.ID, []store.SessionRow{
		{Name: "run1", Kind: "claude", State: "stopped", Origin: "schedule", OriginSession: "parent", ScheduleID: "sch-1"},
		{Name: "plain", Kind: "claude", State: "stopped"},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListSessions(ctx, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]store.SessionRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	// memberSessions' fallback: the mirror row as a wire row.
	mir := mirroredAdminSession(byName["run1"])
	if mir.Origin != "schedule" || mir.OriginSession != "parent" || mir.ScheduleID != "sch-1" {
		t.Fatalf("mirrored row = %+v", mir)
	}
	// The overview's fallback and its row copy, mirrored and live.
	over := mgr.sessionsForOverview(ctx, ws, stubRuntime{}, "stopped")
	live := sessionWire{Name: "live", Alive: true, Origin: "schedule", ScheduleID: "sch-2"}
	for _, c := range []struct {
		w           sessionWire
		origin, sch string
		wantInJSON  bool
	}{
		{mirroredAdminSession(byName["run1"]), "schedule", "sch-1", true},
		{live, "schedule", "sch-2", true},
		{mirroredAdminSession(byName["plain"]), "", "", false},
	} {
		row := adminSessionRowOf("sales", "w", "w@acme.co.jp", "stopped", c.w)
		if row.Origin != c.origin || row.ScheduleID != c.sch {
			t.Errorf("row %s = %+v, want origin %q scheduleId %q", c.w.Name, row, c.origin, c.sch)
		}
		b, _ := json.Marshal(row)
		if got := strings.Contains(string(b), `"scheduleId"`) || strings.Contains(string(b), `"origin"`); got != c.wantInJSON {
			t.Errorf("row %s JSON %s: origin/scheduleId present = %v, want %v", c.w.Name, b, got, c.wantInJSON)
		}
	}
	found := false
	for _, w := range over {
		if w.Name == "run1" {
			found = w.Origin == "schedule" && w.ScheduleID == "sch-1" && w.State == "stopped"
		}
	}
	if !found {
		t.Fatalf("overview fallback = %+v, want run1 with origin, scheduleId and its mirrored state", over)
	}
}

// The queue category id round-trips the DB mirror and reaches both admin session lists, so a
// stopped queue run still names its category; an empty one stays out of the JSON.
func TestQueueCategoryIDRoundTripsTheMirrorAndAdminRows(t *testing.T) {
	ctx := context.Background()
	st, _, _, _, ws := usageHourFixture(t)
	if err := st.ReplaceSessions(ctx, ws.ID, []store.SessionRow{
		{Name: "qrun", Kind: "claude", State: "stopped", Origin: "queue", QueueCategoryID: "cat-1"},
		{Name: "plain", Kind: "claude", State: "stopped"},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListSessions(ctx, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]store.SessionRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	if got := byName["qrun"].QueueCategoryID; got != "cat-1" {
		t.Fatalf("mirror QueueCategoryID = %q, want cat-1", got)
	}
	mir := mirroredAdminSession(byName["qrun"])
	if mir.QueueCategoryID != "cat-1" {
		t.Fatalf("mirrored admin row = %+v", mir)
	}
	row := adminSessionRowOf("sales", "w", "w@acme.co.jp", "stopped", mir)
	b, _ := json.Marshal(row)
	if row.QueueCategoryID != "cat-1" || !strings.Contains(string(b), `"queueCategoryId":"cat-1"`) {
		t.Fatalf("admin row = %s", b)
	}
	b, _ = json.Marshal(adminSessionRowOf("sales", "w", "w@acme.co.jp", "stopped", mirroredAdminSession(byName["plain"])))
	if strings.Contains(string(b), "queueCategoryId") {
		t.Fatalf("empty id leaked into %s", b)
	}
}

// The create the Control Plane sends for a queue fire names the delivering category, which the
// Agent stores as the session's queue category id.
func TestMemoDeliveryCreateBodyNamesTheCategory(t *testing.T) {
	body := memoDeliveryCreateBody(store.MemoDelivery{ID: "d1"}, memoSendPlan{c: store.MemoCategory{ID: "cat-1", Name: "n"}})
	var got struct {
		QueueDelivery struct {
			Queue string `json:"queue"`
		} `json:"queue_delivery"`
	}
	if err := json.Unmarshal(body, &got); err != nil || got.QueueDelivery.Queue != "cat-1" {
		t.Fatalf("queue_delivery.queue = %q (%v), want cat-1", got.QueueDelivery.Queue, err)
	}
}

// A queue run's category goes Agent response -> mirror (sessionRows) -> the stopped Workspace's
// ordinary list (sessionsShared), so a run does not drop into "Other runs" when the Workspace stops.
func TestQueueCategoryIDSurvivesTheMirrorIntoTheStoppedList(t *testing.T) {
	ctx := context.Background()
	_, mgr, _, _, ws := usageHourFixture(t)
	srv := agentSessionsServer(t, `{"sessions":[
		{"name":"qrun","alive":true,"state":"working","kind":"claude","origin":"queue","queueCategoryId":"cat-1"},
		{"name":"plain","alive":true,"state":"working","kind":"claude"}
	]}`)
	mgr.rtFactory = stubFactory{rt: stubRuntime{endpoint: srv.URL}}
	newUsageSampler(mgr, 5*time.Minute).sample(ctx)

	api := workspaceAPI{memberAuth: memberAuth{mgr: mgr}}
	got := map[string]string{}
	for _, s := range api.sessionsShared(ctx, &resolved{rt: stubRuntime{state: "stopped"}, ws: ws}) {
		got[s.Name] = s.QueueCategoryID
	}
	if got["qrun"] != "cat-1" || got["plain"] != "" || len(got) != 2 {
		t.Fatalf("stopped list category ids = %v, want qrun=cat-1 and plain empty", got)
	}
}
