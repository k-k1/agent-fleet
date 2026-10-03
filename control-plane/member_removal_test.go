package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/mcpsrv"
	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
	"github.com/k-k1/agent-fleet/control-plane/internal/tenantsrv"
)

// What a removed member's still-running workspace can reach (issue #1087). The workspace
// holds one bearer token per Agent → CP route family, and workspaceRoutes is the list of
// every route it calls. Each one must refuse the token of a membership that was removed —
// on the very next request, with no restart and no token rotation — and accept it again
// once the person is re-invited.
//
// The table is driven by workspaceRoutes itself, so a route added there without a sample
// here fails the test rather than going unchecked.
func TestRemovedMemberIsRefusedOnEveryWorkspaceRoute(t *testing.T) {
	setRouteSwitches(t, allRouteSwitches(t)...)
	restoreAuthExemptions(t)
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	dt, err := st.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mgr := &manager{
		rts: map[string]cachedRT{}, store: st, dataRoot: t.TempDir(),
		authMode: "dev", devUser: "smoke", provisionMode: "auto", defaultTenantID: dt.ID,
		conns: newConnRegistry(), master32: []byte("master-key-member-removal-0000000"),
		publicBaseURL: "https://af.example", rtFactory: &stopRecordingFactory{},
	}
	cfg := config{consoleDir: t.TempDir(), mgr: mgr, egressDedup: &egressAuditDedup{},
		publicBaseURL: "https://af.example"}
	mux := buildMux(cfg)
	h := workspaceListenerHandler(mux, mgr.emailHeader)

	ident, _ := st.UpsertIdentity(ctx, "leaver@x", "leaver-x", "")
	mem, err := st.EnsureMembership(ctx, ident.ID, dt.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	master := mgr.tokenSignMaster()
	bearer := func(tok string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
	}
	gitAuth := func(r *http.Request) { r.SetBasicAuth("x-access-token", mintGitToken(gitSignKey(master), mem.ID)) }
	families := []struct {
		prefix string
		auth   func(*http.Request)
	}{
		{"/internal/docs", bearer(mintDocsToken(docsSignKey(master), mem.ID))},
		{"/internal/branch-rules", bearer(mintBranchRulesToken(branchRulesSignKey(master), mem.ID))},
		{"/internal/mcp-servers", bearer(mcpsrv.MintMCPToken(mcpsrv.MCPSignKey(master), mem.ID))},
		{"/internal/aws-profiles", bearer(mintAWSProfilesToken(awsProfilesSignKey(master), mem.ID))},
		{"/internal/gcp-profiles", bearer(mintGCPProfilesToken(gcpProfilesSignKey(master), mem.ID))},
		{"/internal/memos", bearer(mintMemoToken(memoSignKey(master), mem.ID))},
		{"/internal/schedules", bearer(mintScheduleToken(scheduleSignKey(master), mem.ID))},
		{"/internal/git-oauth/", bearer(mintGitOAuthToken(gitOAuthSignKey(master), mem.ID))},
		{"/internal/engine/", bearer(mintEngineIssueToken(engineSignKey(master), mem.ID))},
		{"/engine/llm/", bearer(mintEngineSessionToken(engineSignKey(master), mem.ID, "s1", "llm", time.Now().Add(time.Hour)))},
		{"/git/", gitAuth},
	}
	// One request per workspaceRoutes pattern, as the Agent sends it.
	samples := []struct{ method, path string }{
		{"GET", "/internal/docs"},
		{"GET", "/internal/branch-rules"},
		{"GET", "/internal/mcp-servers"},
		{"GET", "/internal/aws-profiles"},
		{"GET", "/internal/gcp-profiles"},
		{"GET", "/internal/memos"},
		{"POST", "/internal/memos"},
		{"POST", "/internal/memos/flush"},
		{"PATCH", "/internal/memos/m1"},
		{"DELETE", "/internal/memos/m1"},
		{"GET", "/internal/schedules"},
		{"POST", "/internal/schedules"},
		{"PATCH", "/internal/schedules/s1"},
		{"DELETE", "/internal/schedules/s1"},
		{"POST", "/internal/schedules/s1/pause"},
		{"POST", "/internal/schedules/s1/resume"},
		{"POST", "/internal/schedules/s1/run-now"},
		{"GET", "/internal/schedules/s1/runs"},
		{"POST", "/internal/git-oauth/bitbucket/refresh"},
		{"POST", "/internal/git-oauth/jira/refresh"},
		{"POST", "/internal/engine/token"},
		{"GET", "/internal/engine/catalog"},
		{"GET", "/engine/llm/props"},
		{"POST", "/engine/llm/v1/chat/completions"},
		{"GET", "/git/default/r.git/info/refs?service=git-upload-pack"},
		{"POST", "/git/default/r.git/info/lfs/objects/batch"},
		{"PUT", "/git/default/r.git/info/lfs/objects/" + strings.Repeat("a", 64)},
		{"GET", "/git/default/r.git/info/lfs/objects/" + strings.Repeat("a", 64)},
		{"POST", "/git/default/r.git/info/lfs/locks"},
		{"GET", "/git/default/r.git/info/lfs/locks"},
		{"POST", "/git/default/r.git/info/lfs/locks/verify"},
		{"POST", "/git/default/r.git/info/lfs/locks/l1/unlock"},
	}
	send := func(method, path string) (pattern string, code int, body string) {
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		var auth func(*http.Request)
		for _, f := range families {
			if strings.HasPrefix(req.URL.Path, f.prefix) {
				auth = f.auth
				break
			}
		}
		if auth == nil {
			t.Fatalf("%s %s: no token family for this path", method, path)
		}
		auth(req)
		_, pattern = mux.Handler(req)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return pattern, w.Code, w.Body.String()
	}

	covered := map[string]bool{}
	phase := func(name string, wantRefused bool) {
		for _, s := range samples {
			pattern, code, body := send(s.method, s.path)
			covered[pattern] = true
			refused := code == http.StatusUnauthorized
			if refused != wantRefused {
				t.Errorf("%s: %s %s (pattern %q) = %d %s", name, s.method, s.path, pattern, code, strings.TrimSpace(body))
			}
		}
	}
	// Positive control: the same tokens get past authentication while the member is active
	// (whatever the handler answers next), so a 401 below is the removal and nothing else.
	phase("active member", false)
	if err := st.SetMembershipStatus(ctx, mem.ID, "inactive"); err != nil {
		t.Fatal(err)
	}
	mgr.evictMembershipCache(mem.ID)
	phase("removed member", true)
	if err := st.SetMembershipStatus(ctx, mem.ID, "active"); err != nil {
		t.Fatal(err)
	}
	phase("restored member", false)

	for p := range workspaceRoutes {
		if !covered[p] {
			t.Errorf("workspaceRoutes pattern %q has no sample here: add the Agent's request for it", p)
		}
	}
}

// stopRecordingFactory hands out runtimes that record which containers were stopped, and
// can hold a Stop until released, standing in for a slow ECS drain.
type stopRecordingFactory struct {
	mu      sync.Mutex
	stopped []string
	hold    chan struct{} // nil: Stop returns at once
}

func (f *stopRecordingFactory) New(ws runtime.Workspace, _ string, _ []string) runtime.Runtime {
	return stopRecordingRuntime{f: f, name: ws.ContainerName}
}

func (f *stopRecordingFactory) stops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.stopped...)
}

type stopRecordingRuntime struct {
	stubRuntime
	f    *stopRecordingFactory
	name string
}

func (r stopRecordingRuntime) Stop(ctx context.Context) error {
	if r.f.hold != nil {
		select {
		case <-r.f.hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r.f.mu.Lock()
	r.f.stopped = append(r.f.stopped, r.name)
	r.f.mu.Unlock()
	return nil
}

// A person in two tenants: removed from sales, still a member of dev.
func twoTenantFixture(t *testing.T, f runtime.RuntimeFactory) (*store.SQL, *manager, store.Identity, store.Tenant, store.Tenant) {
	t.Helper()
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	mgr.rtFactory = f
	sales, _ := st.CreateTenant(ctx, "sales", "Sales")
	dev, _ := st.CreateTenant(ctx, "dev", "Dev")
	admin, _ := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "super_admin")
	victim, _ := st.UpsertIdentity(ctx, "leaver@acme.co.jp", "leaver-acme-co-jp", "")
	for i, tn := range []store.Tenant{sales, dev} {
		if _, err := st.EnsureMembership(ctx, admin.ID, tn.ID, "tenant_admin"); err != nil {
			t.Fatal(err)
		}
		mem, err := st.EnsureMembership(ctx, victim.ID, tn.ID, "member")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.CreateWorkspace(ctx, store.Workspace{
			ID: "W-" + tn.Slug, TenantID: tn.ID, MembershipID: mem.ID,
			ContainerName: "af-ws-" + tn.Slug + "-leaver", Network: "af-net-" + tn.Slug,
			DataDir: "/srv/data/" + tn.Slug + "/leaver", AgentPort: "773" + string(rune('1'+i)),
			AgentToken: "tok", State: "running", CreatedAt: store.NowTS(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	return st, mgr, victim, sales, dev
}

func callAddMembership(mgr *manager, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	newAdminAPI(mgr).addMembership(w, adminRequest(http.MethodPost, "/api/admin/memberships", body))
	return w
}

func auditWith(t *testing.T, st *store.SQL, tenantID, action string) []store.AuditLog {
	t.Helper()
	rows, err := st.ListAuditByTenant(context.Background(), tenantID, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditLog
	for _, a := range rows {
		if a.Action == action {
			out = append(out, a)
		}
	}
	return out
}

// Removing a member stops the workspace they had in that tenant — and only that one: the
// same person's workspace in another tenant is their desk there and keeps running.
func TestRemoveMembershipStopsOnlyThatTenantsWorkspace(t *testing.T) {
	f := &stopRecordingFactory{}
	st, mgr, _, sales, _ := twoTenantFixture(t, f)
	w := callRemoveMembership(mgr, `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		WorkspaceStop string `json:"workspace_stop"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.WorkspaceStop != "stopped" {
		t.Errorf("workspace_stop = %q, want stopped", resp.WorkspaceStop)
	}
	if got := f.stops(); len(got) != 1 || got[0] != "af-ws-sales-leaver" {
		t.Errorf("stopped containers = %v, want only af-ws-sales-leaver", got)
	}
	ws, _, _ := st.GetWorkspaceByMembership(context.Background(), mustMembershipID(t, st, "leaver-acme-co-jp", sales))
	if ws.State != "stopped" {
		t.Errorf("sales workspace state = %q, want stopped", ws.State)
	}
	rows := auditWith(t, st, sales.ID, "membership.remove")
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "workspace stop stopped") {
		t.Errorf("membership.remove audit = %+v, want the stop recorded", rows)
	}
}

// A stop the runtime takes its time over (an ECS drain) must not hold the administrator's
// request: the removal answers "pending", and the stop's outcome lands in the audit log
// when it finishes.
func TestRemoveMembershipDoesNotWaitForASlowStop(t *testing.T) {
	old := tenantsrv.RemovalStopWait
	tenantsrv.RemovalStopWait = 20 * time.Millisecond
	t.Cleanup(func() { tenantsrv.RemovalStopWait = old })
	f := &stopRecordingFactory{hold: make(chan struct{})}
	st, mgr, victim, sales, _ := twoTenantFixture(t, f)

	start := time.Now()
	w := callRemoveMembership(mgr, `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"workspace_stop":"pending"`) {
		t.Fatalf("remove: %d %s, want 200 with workspace_stop pending", w.Code, w.Body.String())
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the removal waited %s for the stop", d)
	}
	// The person is locked out already, whatever the stop is doing.
	if got, _, _ := st.GetMembership(context.Background(), victim.ID, sales.ID); got.Status != "inactive" {
		t.Fatalf("membership status = %q while the stop runs, want inactive", got.Status)
	}
	if n := len(auditWith(t, st, sales.ID, "membership.remove.stop_workspace")); n != 0 {
		t.Fatalf("stop outcome audited before the stop finished (%d rows)", n)
	}
	close(f.hold)
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows := auditWith(t, st, sales.ID, "membership.remove.stop_workspace")
		if len(rows) == 1 {
			if rows[0].Detail != "workspace stop stopped" || rows[0].Target != "leaver-acme-co-jp" {
				t.Errorf("stop outcome audit = %+v", rows[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the background stop never recorded its outcome")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := f.stops(); len(got) != 1 || got[0] != "af-ws-sales-leaver" {
		t.Errorf("stopped containers = %v, want only af-ws-sales-leaver", got)
	}
}

func mustMembershipID(t *testing.T, st *store.SQL, key string, tn store.Tenant) string {
	t.Helper()
	ident, ok, err := st.GetIdentityByUserKey(context.Background(), key)
	if err != nil || !ok {
		t.Fatalf("identity %s: %v", key, err)
	}
	return membershipIDOf(t, st, ident, tn)
}

// Scheduled runs of a removed member: the next due slot fires nothing (no workspace is
// woken) and pauses the schedule; re-inviting the person resumes exactly the schedules
// that removal paused, from now on, and leaves alone one they had paused themselves.
func TestRemovedMemberSchedulesPauseAndResumeOnRestore(t *testing.T) {
	ctx := context.Background()
	f := &stopRecordingFactory{}
	st, mgr, victim, sales, _ := twoTenantFixture(t, f)
	mid := membershipIDOf(t, st, victim, sales)
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	mk := func(id string, enabled bool) {
		if err := st.CreateSchedule(ctx, store.Schedule{
			ID: id, MembershipID: mid, SpecKind: "interval", Spec: "3600", TZ: "UTC",
			WakePolicy: "wake", SessionMode: "new", AgentKind: "claude", Prompt: "p",
			Enabled: enabled, NextRun: past, CreatedAt: past, UpdatedAt: past,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mk("S-due", true)
	mk("S-paused-by-owner", false)

	if w := callRemoveMembership(mgr, `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`); w.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	stopsAfterRemoval := len(f.stops())

	sc := newScheduler(st, newWakeFirer(mgr, 0, time.Second), time.Minute)
	sc.tickAt(ctx, time.Now().UTC())
	got, _, _ := st.GetSchedule(ctx, "S-due")
	if got.Enabled || got.LastStatus != statusMembershipInactive {
		t.Fatalf("after a due slot while removed: enabled=%v last_status=%q, want paused with %s",
			got.Enabled, got.LastStatus, statusMembershipInactive)
	}
	if n := len(f.stops()); n != stopsAfterRemoval {
		t.Errorf("the firer touched a runtime for a removed member")
	}

	if w := callAddMembership(mgr, `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`); w.Code != http.StatusOK {
		t.Fatalf("re-invite: %d %s", w.Code, w.Body.String())
	}
	got, _, _ = st.GetSchedule(ctx, "S-due")
	if !got.Enabled {
		t.Fatal("the schedule removal paused is still paused after the restore")
	}
	if next, err := time.Parse(time.RFC3339, got.NextRun); err != nil || !next.After(time.Now()) {
		t.Errorf("next_run = %q after the restore, want a future slot (missed slots are not replayed)", got.NextRun)
	}
	if own, _, _ := st.GetSchedule(ctx, "S-paused-by-owner"); own.Enabled {
		t.Error("the restore resumed a schedule its owner had paused")
	}
	rows := auditWith(t, st, sales.ID, "membership.add")
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "schedules resumed=1") {
		t.Errorf("membership.add audit = %+v, want the resume recorded", rows)
	}
	// Restored means the workspace tokens work again: they are the same deterministic
	// tokens and are judged against the live row.
	if _, ok, _ := st.GetMembershipByID(ctx, mid); !ok {
		t.Error("membership not active after the restore")
	}
}
