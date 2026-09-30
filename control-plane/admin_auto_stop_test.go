package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// memberRows reads GET /api/admin/tenants/sales/members as email and returns the rows by
// user_key.
func memberRows(t *testing.T, mgr *manager, email string) map[string]map[string]any {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/admin/tenants/sales/members", nil)
	r.SetPathValue("slug", "sales")
	r.Header.Set("X-Forwarded-Email", email)
	w := httptest.NewRecorder()
	newAdminAPI(mgr).listMembers(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET members = %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Members []map[string]any `json:"members"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, m := range body.Members {
		out[m["user_key"].(string)] = m
	}
	return out
}

// A launch the start deadline stopped reads as plain "stopped" on the admin roster unless
// the reason is kept: the notification reaches the member alone (#1384). The reason lasts
// until the next start, and a launch still in flight is not reported as stopped.
func TestAdminMembersShowTheStartDeadlineStop(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, mv, _ := networkFixture(t)
	ws := store.Workspace{ID: store.NewID(), TenantID: tn.ID, MembershipID: mv.MembershipID,
		ContainerName: "af-yamada", Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t",
		State: "running", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	setLastActive(t, st, ws.ID, time.Now().Add(-2*time.Hour))
	rt := &deadlineStub{state: "starting"}
	mgr.rtFactory = stubFactory{rt: rt}
	d := newStartDeadline(mgr, 30*time.Minute)
	d.seen[ws.ID] = time.Now().Add(-time.Hour)
	if !d.stop(ctx, rt, ws) || rt.stops.Load() != 1 {
		t.Fatalf("the overdue launch was not stopped (Stop calls %d)", rt.stops.Load())
	}

	row := memberRows(t, mgr, "boss@acme.co.jp")["yamada-acme-co-jp"]
	as, _ := row["auto_stop"].(map[string]any)
	if row["state"] != "stopped" || as == nil {
		t.Fatalf("member row = %v, want state stopped with an auto_stop", row)
	}
	if as["kind"] != "start-deadline" || as["phase"] != "blocked: no container instance met all of its requirements" ||
		as["limit_minutes"] != float64(30) || as["stopped_at"] == "" {
		t.Errorf("auto_stop = %v, want the kind, the last phase, the limit and the time", as)
	}

	// Relaunched: the runtime says starting before any SetWorkspaceState("running").
	rt.state = "starting"
	if _, ok := memberRows(t, mgr, "boss@acme.co.jp")["yamada-acme-co-jp"]["auto_stop"]; ok {
		t.Error("a launch in flight is still reported as auto-stopped")
	}

	// The next start reaches running and deletes the record for good, so a later ordinary
	// stop does not carry the old reason.
	if err := st.SetWorkspaceState(ctx, ws.ID, "running"); err != nil {
		t.Fatal(err)
	}
	rt.state = "stopped"
	if _, ok := memberRows(t, mgr, "boss@acme.co.jp")["yamada-acme-co-jp"]["auto_stop"]; ok {
		t.Error("the reason survived the next start")
	}
	if _, ok, err := st.GetWorkspaceAutoStopByMembership(ctx, mv.MembershipID); ok || err != nil {
		t.Errorf("record after the next start: ok=%v err=%v, want deleted", ok, err)
	}

	// Destroying the workspace takes the record with it.
	if err := st.SetWorkspaceAutoStop(ctx, ws.ID, store.WorkspaceAutoStop{Kind: "start-deadline", StoppedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteWorkspace(ctx, ws.ID); err != nil {
		t.Fatalf("DeleteWorkspace with an auto-stop record: %v", err)
	}
	var n int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_auto_stop`).Scan(&n); err != nil || n != 0 {
		t.Errorf("auto-stop rows after DeleteWorkspace = %d (err %v), want 0", n, err)
	}
}

// failStartStub is a stopped workspace whose next Start fails, as an ecs-ec2 Start does when
// preparing secrets or the home fails after the launch began.
type failStartStub struct{ deadlineStub }

func (r *failStartStub) Start(context.Context) error { return errors.New("prepare secrets: denied") }

// A new launch is a new story whether or not it succeeds: a Start that fails must not leave
// the admin reading the previous launch's deadline stop as the reason it is down.
func TestAFailedStartClearsThePreviousAutoStop(t *testing.T) {
	ctx := context.Background()
	st, ws, mgr := reaperLifecycleFixture(t)
	if err := st.SetWorkspaceState(ctx, ws.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetWorkspaceAutoStop(ctx, ws.ID, store.WorkspaceAutoStop{Kind: "start-deadline", Phase: "blocked: x", StoppedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	rt := &failStartStub{deadlineStub{state: "stopped"}}
	res := &resolved{rt: rt, ws: ws, mv: store.MembershipView{MembershipID: ws.MembershipID, TenantID: ws.TenantID}}
	if aerr := newWorkspaceAPI(mgr, false).ensureWorkspaceStartedRT(ctx, res, rt); aerr == nil {
		t.Fatal("the Start did not fail, the test proves nothing")
	}
	if as := store.CurrentAutoStop(ctx, st, ws.MembershipID, rt.State(ctx)); as != nil {
		t.Errorf("after a failed Start the admin still reads %+v", *as)
	}
}

// The member detail polls /stats and reads the reason from there: the roster row it was
// opened from is a snapshot that would keep the reason after the member restarted.
func TestMemberStatsCarriesTheAutoStopOnlyWhileDown(t *testing.T) {
	for _, c := range []struct {
		state string
		want  bool
	}{{"stopped", true}, {"starting", false}, {"running", false}} {
		st, mgr, _, _ := destroyFixture(t, stateOnlyFactory{state: c.state})
		if err := st.SetWorkspaceAutoStop(context.Background(), "W-1",
			store.WorkspaceAutoStop{Kind: "start-deadline", Phase: "blocked: x", LimitMinutes: 30, StoppedAt: store.NowTS()}); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, "/api/admin/tenants/sales/members/leaver-acme-co-jp/stats", nil)
		r.SetPathValue("slug", "sales")
		r.SetPathValue("key", "leaver-acme-co-jp")
		r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
		w := httptest.NewRecorder()
		newAdminAPI(mgr).memberStats(w, r)
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("state=%s: %d %v", c.state, w.Code, err)
		}
		as, _ := out["auto_stop"].(map[string]any)
		if got := as != nil; got != c.want {
			t.Errorf("state=%s: auto_stop=%v, want present=%v", c.state, out["auto_stop"], c.want)
		}
		if as != nil && (as["kind"] != "start-deadline" || as["phase"] != "blocked: x") {
			t.Errorf("state=%s: auto_stop=%v", c.state, as)
		}
	}
}
