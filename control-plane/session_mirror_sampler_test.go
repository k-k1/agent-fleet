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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
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
