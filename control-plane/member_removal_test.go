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
	gitAuth := func(r *http.Request) { r.SetBasicAuth("x-access-token", mintGitToken(gitSignKey(master), mem.ID, 0)) }
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

// waitAudit polls until tenantID has n rows of action, and returns them.
func waitAudit(t *testing.T, st *store.SQL, tenantID, action string, n int) []store.AuditLog {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows := auditWith(t, st, tenantID, action)
		if len(rows) >= n {
			return rows
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d %s audit row(s) never appeared (have %d)", n, action, len(rows))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A removal whose stop is queued behind a start (the workspace was booting) must not stop
// the workspace of somebody re-invited before the stop got the lock: the stop re-reads the
// membership once it holds the workspace, and leaves a restored person's workspace alone.
func TestRemovalStopQueuedBehindAStartSparesARestoredMember(t *testing.T) {
	old := tenantsrv.RemovalStopWait
	tenantsrv.RemovalStopWait = 20 * time.Millisecond
	t.Cleanup(func() { tenantsrv.RemovalStopWait = old })
	f := &stopRecordingFactory{}
	st, mgr, _, sales, _ := twoTenantFixture(t, f)

	start := mgr.startLockFor("W-sales") // a start in flight holds this
	start.Lock()
	w := callRemoveMembership(mgr, `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`)
	if !strings.Contains(w.Body.String(), `"workspace_stop":"pending"`) {
		t.Fatalf("remove: %d %s, want the stop pending behind the start", w.Code, w.Body.String())
	}
	if w := callAddMembership(mgr, `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`); w.Code != http.StatusOK {
		t.Fatalf("re-invite: %d %s", w.Code, w.Body.String())
	}
	start.Unlock() // the start finishes; the queued removal stop runs now

	rows := waitAudit(t, st, sales.ID, "membership.remove.stop_workspace", 1)
	if rows[0].Detail != "workspace stop skipped: membership restored" {
		t.Errorf("stop outcome = %q, want it skipped", rows[0].Detail)
	}
	if got := f.stops(); len(got) != 0 {
		t.Errorf("stopped %v after the person was re-invited", got)
	}
}

// Removed, restored and removed again while the first stop is still queued: both stops
// act on the status they read under the lock, which is the second removal's.
func TestRemovalStopAfterRemoveRestoreRemoveStops(t *testing.T) {
	old := tenantsrv.RemovalStopWait
	tenantsrv.RemovalStopWait = 20 * time.Millisecond
	t.Cleanup(func() { tenantsrv.RemovalStopWait = old })
	f := &stopRecordingFactory{}
	st, mgr, _, sales, _ := twoTenantFixture(t, f)

	start := mgr.startLockFor("W-sales")
	start.Lock()
	body := `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`
	callRemoveMembership(mgr, body)
	callAddMembership(mgr, body)
	callRemoveMembership(mgr, body)
	start.Unlock()

	rows := waitAudit(t, st, sales.ID, "membership.remove.stop_workspace", 2)
	for _, r := range rows {
		if r.Detail != "workspace stop stopped" {
			t.Errorf("stop outcome = %q, want stopped (the membership is inactive again)", r.Detail)
		}
	}
	if got := f.stops(); len(got) == 0 || got[0] != "af-ws-sales-leaver" {
		t.Errorf("stopped %v, want af-ws-sales-leaver", got)
	}
}

// The removal's own stop lives in one process. A CP restart before it ran, or a stop that
// ran out of budget, leaves an inactive membership's workspace running; the sweep finds it
// by the row and stops it, and leaves active members' workspaces alone.
func TestRemovedMemberSweepStopsWhatTheRemovalLeftRunning(t *testing.T) {
	ctx := context.Background()
	f := &stopRecordingFactory{}
	st, mgr, victim, sales, _ := twoTenantFixture(t, f)
	// The removal as it looks after a CP restart: the row is inactive, nothing ran.
	if err := st.SetMembershipStatus(ctx, membershipIDOf(t, st, victim, sales), "inactive"); err != nil {
		t.Fatal(err)
	}
	mgr.sweepRemovedMemberWorkspaces(ctx).Wait()
	if got := f.stops(); len(got) != 1 || got[0] != "af-ws-sales-leaver" {
		t.Fatalf("sweep stopped %v, want only af-ws-sales-leaver", got)
	}
	if rows := auditWith(t, st, sales.ID, "membership.remove.stop_workspace"); len(rows) != 1 {
		t.Errorf("sweep audit rows = %+v, want one", rows)
	}
	mgr.sweepRemovedMemberWorkspaces(ctx).Wait() // stopped rows are not stopped again
	if got := f.stops(); len(got) != 1 {
		t.Errorf("second sweep stopped again: %v", got)
	}
}

// A schedule's owner pause must survive a later remove/restore, even when every write lands
// in the same second (the timestamps cannot tell them apart; the mark can).
func TestRestoreLeavesAnOwnerPauseFromTheSameSecond(t *testing.T) {
	ctx := context.Background()
	st, mgr, victim, sales, _ := twoTenantFixture(t, &stopRecordingFactory{})
	mid := membershipIDOf(t, st, victim, sales)
	now := store.NowTS()
	if err := st.CreateSchedule(ctx, store.Schedule{ID: "S", MembershipID: mid, SpecKind: "interval", Spec: "3600",
		TZ: "UTC", Enabled: true, NextRun: now, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	body := `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`
	callRemoveMembership(mgr, body)
	if held, err := st.HoldScheduleForRemoval(ctx, "S", now, now, statusMembershipInactive, now); err != nil || !held {
		t.Fatalf("hold: %v %v", held, err)
	}
	callAddMembership(mgr, body)
	if got, _, _ := st.GetSchedule(ctx, "S"); !got.Enabled {
		t.Fatal("restore did not resume the held schedule")
	}
	// The owner pauses it, stamped with the same second as the hold.
	if err := st.SetScheduleEnabled(ctx, "S", mid, false, "", now); err != nil {
		t.Fatal(err)
	}
	callRemoveMembership(mgr, body)
	callAddMembership(mgr, body)
	if got, _, _ := st.GetSchedule(ctx, "S"); got.Enabled {
		t.Error("a second restore resumed the schedule the owner had paused")
	}
}

// The resume is conditional: an owner's pause between the restore's read and its write wins.
func TestResumeHeldScheduleLosesToAnOwnerPause(t *testing.T) {
	ctx := context.Background()
	st, _, victim, sales, _ := twoTenantFixture(t, &stopRecordingFactory{})
	mid := membershipIDOf(t, st, victim, sales)
	now := store.NowTS()
	_ = st.CreateSchedule(ctx, store.Schedule{ID: "S", MembershipID: mid, SpecKind: "interval", Spec: "3600",
		TZ: "UTC", Enabled: true, NextRun: now, CreatedAt: now, UpdatedAt: now})
	_ = st.SetMembershipStatus(ctx, mid, "inactive")
	if held, _ := st.HoldScheduleForRemoval(ctx, "S", now, now, statusMembershipInactive, now); !held {
		t.Fatal("not held")
	}
	read, _, _ := st.GetSchedule(ctx, "S") // the restore's read
	if err := st.SetScheduleEnabled(ctx, "S", mid, false, "", now); err != nil {
		t.Fatal(err) // the owner's pause lands in between
	}
	if ok, err := resumeHeldSchedule(ctx, st, read, time.Now().UTC()); err != nil || ok {
		t.Fatalf("resume over an owner pause = %v %v, want no-op", ok, err)
	}
	if got, _, _ := st.GetSchedule(ctx, "S"); got.Enabled {
		t.Error("the owner's pause was overwritten")
	}
}

// holdRacingStore lets the restore commit at the worst moment for the scheduler: right
// after its hold was written, before it reads the membership again — the order in which
// the restore's scan finds nothing held.
type holdRacingStore struct {
	*store.SQL
	restore func()
}

func (s holdRacingStore) HoldScheduleForRemoval(ctx context.Context, id, slot, lastRun, lastStatus, updatedAt string) (bool, error) {
	held, err := s.SQL.HoldScheduleForRemoval(ctx, id, slot, lastRun, lastStatus, updatedAt)
	s.restore()
	return held, err
}

// The scheduler decided "inactive", then the person was re-invited before its pause was
// written (no pause is written), or while it was being written (the pause is undone).
// Either way a restored owner never keeps a paused schedule.
func TestSchedulerPauseRacingARestore(t *testing.T) {
	ctx := context.Background()
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	setup := func(t *testing.T) (*store.SQL, string) {
		st, _, victim, sales, _ := twoTenantFixture(t, &stopRecordingFactory{})
		mid := membershipIDOf(t, st, victim, sales)
		if err := st.CreateSchedule(ctx, store.Schedule{ID: "S", MembershipID: mid, SpecKind: "interval", Spec: "3600",
			TZ: "UTC", Enabled: true, NextRun: past, CreatedAt: past, UpdatedAt: past}); err != nil {
			t.Fatal(err)
		}
		return st, mid
	}
	t.Run("restored before the pause is written", func(t *testing.T) {
		st, _ := setup(t) // membership active: the firer's verdict is stale
		sc := newScheduler(st, &fakeFirer{status: statusMembershipInactive}, time.Minute)
		sc.tickAt(ctx, time.Now().UTC())
		got, _, _ := st.GetSchedule(ctx, "S")
		if !got.Enabled || got.HeldByRemoval {
			t.Errorf("schedule = enabled %v held %v, want left enabled for the next tick", got.Enabled, got.HeldByRemoval)
		}
	})
	t.Run("restored while the pause is written", func(t *testing.T) {
		st, mid := setup(t)
		_ = st.SetMembershipStatus(ctx, mid, "inactive")
		racing := holdRacingStore{SQL: st, restore: func() { _ = st.SetMembershipStatus(ctx, mid, "active") }}
		sc := newScheduler(racing, &fakeFirer{status: statusMembershipInactive}, time.Minute)
		sc.tickAt(ctx, time.Now().UTC())
		got, _, _ := st.GetSchedule(ctx, "S")
		if !got.Enabled || got.HeldByRemoval {
			t.Errorf("schedule = enabled %v held %v, want resumed after the racing restore", got.Enabled, got.HeldByRemoval)
		}
	})
}

// hostPreviewCookie mints the af_pv cookie the handshake would have set for membershipID.
func hostPreviewCookie(e *previewHostEnv, slug string, port int, membershipID string) *http.Cookie {
	v := newPreviewHostAPI(e.cfg).sign(previewClaims{Slug: slug, Port: port, MembershipID: membershipID,
		Exp: time.Now().Add(time.Hour).Unix()})
	return &http.Cookie{Name: previewAuthCookie, Value: v}
}

// The owner's own preview cookie is judged against the live membership like anyone
// else's, and a removed owner's workspace serves nobody — public mode included — while
// its stop is pending or after it failed.
func TestPreviewOfARemovedOwnerServesNobody(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer agent.Close()
	e := newPreviewHostEnv(t, agent.URL)
	slug := e.mintSlug(t)
	ctx := context.Background()
	ck := hostPreviewCookie(e, slug, 3000, e.ws.MembershipID)
	if rec := e.get(t, slug, 3000, "/", ck); rec.Code != http.StatusOK {
		t.Fatalf("control: owner with cookie = %d", rec.Code)
	}
	_ = e.mgr.store.SetMembershipStatus(ctx, e.ws.MembershipID, "inactive")
	if rec := e.get(t, slug, 3000, "/", ck); rec.Code == http.StatusOK {
		t.Errorf("removed owner's cookie still served: %d", rec.Code)
	}
	raw, _ := e.mgr.store.GetWorkspaceSettings(ctx, e.ws.ID)
	st := parseWSSettings(raw)
	st.PreviewPublic = true
	_ = e.mgr.store.SetWorkspaceSettings(ctx, e.ws.ID, toWSSettingsJSON(t, st))
	if rec := e.get(t, slug, 8080, "/"); rec.Code == http.StatusOK {
		t.Errorf("public preview of a removed owner still served: %d", rec.Code)
	}
}

// A stream opened through another member's shared preview before the viewer was removed
// ends when the removal reaches this replica — here through the sweep, which is how every
// replica other than the one serving the removal hears of it. The owner's workspace keeps
// running.
func TestRemovedViewersOpenPreviewStreamIsClosed(t *testing.T) {
	opened := make(chan struct{})
	release := make(chan struct{}) // lets a failing run end instead of hanging agent.Close
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(opened)
		select { // stream until the CP hangs up
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer agent.Close()
	defer close(release)
	e := newPreviewHostEnv(t, agent.URL)
	slug := e.mintSlug(t)
	ctx := context.Background()
	raw, _ := e.mgr.store.GetWorkspaceSettings(ctx, e.ws.ID)
	st := parseWSSettings(raw)
	st.PreviewTenantShare = true
	_ = e.mgr.store.SetWorkspaceSettings(ctx, e.ws.ID, toWSSettingsJSON(t, st))
	viewer, _ := e.mgr.store.UpsertIdentity(ctx, "", "viewer", "")
	vm, err := e.mgr.store.EnsureMembership(ctx, viewer.ID, e.ws.TenantID, "member")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() { done <- e.get(t, slug, 3000, "/events", hostPreviewCookie(e, slug, 3000, vm.ID)).Code }()
	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream never reached the owner's agent")
	}
	_ = e.mgr.store.SetMembershipStatus(ctx, vm.ID, "inactive")
	e.mgr.sweepRemovedMemberConns(ctx)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the removed viewer's stream is still open")
	}
}

// Removing a member closes their tracked requests on the serving replica at once, without
// waiting for the sweep — and only theirs.
func TestRemoveMembershipClosesTheMembersConnections(t *testing.T) {
	st, mgr, victim, sales, dev := twoTenantFixture(t, &stopRecordingFactory{})
	salesCtx, untrackSales := mgr.memberConns.track(context.Background(), membershipIDOf(t, st, victim, sales))
	defer untrackSales()
	devCtx, untrackDev := mgr.memberConns.track(context.Background(), membershipIDOf(t, st, victim, dev))
	defer untrackDev()
	callRemoveMembership(mgr, `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`)
	if salesCtx.Err() == nil {
		t.Error("the removed membership's request is still open")
	}
	if devCtx.Err() != nil {
		t.Error("the same person's request in another tenant was closed")
	}
}

// The owner's pause was authorised before the removal and lands after the scheduler listed
// the row: the stale fire must not stamp a hold on the paused row, or the next re-invite
// would resume what the owner stopped.
func TestSchedulerHoldDoesNotOverwriteAnOwnerPause(t *testing.T) {
	ctx := context.Background()
	st, mgr, victim, sales, _ := twoTenantFixture(t, &stopRecordingFactory{})
	mid := membershipIDOf(t, st, victim, sales)
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	if err := st.CreateSchedule(ctx, store.Schedule{ID: "S", MembershipID: mid, SpecKind: "interval", Spec: "3600",
		TZ: "UTC", Enabled: true, NextRun: past, CreatedAt: past, UpdatedAt: past}); err != nil {
		t.Fatal(err)
	}
	listed, _, _ := st.GetSchedule(ctx, "S") // what the tick listed
	body := `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`
	callRemoveMembership(mgr, body)
	if err := st.SetScheduleEnabled(ctx, "S", mid, false, "", store.NowTS()); err != nil {
		t.Fatal(err)
	}
	sc := newScheduler(st, &fakeFirer{status: statusMembershipInactive}, time.Minute)
	sc.fireOne(ctx, listed, time.Now().UTC())
	if got, _, _ := st.GetSchedule(ctx, "S"); got.HeldByRemoval {
		t.Fatal("the stale fire marked the owner-paused row as held by the removal")
	}
	callAddMembership(mgr, body)
	if got, _, _ := st.GetSchedule(ctx, "S"); got.Enabled {
		t.Error("the re-invite resumed a schedule the owner had paused")
	}
}

// A generation streaming on an engine session token issued before the removal ends when
// the removal reaches this replica — the gateway is not a withResolved route, so it files
// the request itself.
func TestRemovedMembersEngineStreamEnds(t *testing.T) {
	opened := make(chan struct{})
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: before\n\n"))
		w.(http.Flusher).Flush()
		close(opened)
		select {
		case <-r.Context().Done():
		case <-release:
			_, _ = w.Write([]byte("data: after\n\n"))
		}
	}))
	defer up.Close()
	defer close(release)
	g, mids := engineTenantGateFixture(t)
	mid := mids["allowed"]
	g.reg.byKey["llm"].def.URL = up.URL
	req := httptest.NewRequest(http.MethodPost, "/engine/llm/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	req.SetPathValue("key", "llm")
	req.SetPathValue("path", "chat/completions")
	req.Header.Set("Authorization", "Bearer "+mintEngineSessionToken(g.reg.signKey, mid, "s1", "llm", time.Now().Add(time.Hour)))
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { g.serve(rec, req); close(done) }()
	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("control: the stream never reached the engine")
	}
	if err := g.mgr.store.SetMembershipStatus(context.Background(), mid, "inactive"); err != nil {
		t.Fatal(err)
	}
	g.mgr.sweepRemovedMemberConns(context.Background())
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the removed member's engine stream is still open")
	}
	if strings.Contains(rec.Body.String(), "after") {
		t.Errorf("the stream delivered after the removal: %q", rec.Body.String())
	}
}

// One slow workspace stop must not hold up closing the connections of a later removal: the
// connection sweep is its own loop, and stops run off the sweep.
func TestConnectionSweepDoesNotWaitForAWorkspaceStop(t *testing.T) {
	f := &stopRecordingFactory{hold: make(chan struct{})}
	st, mgr, victim, sales, dev := twoTenantFixture(t, f)
	mid := membershipIDOf(t, st, victim, sales)
	other := membershipIDOf(t, st, victim, dev)
	otherCtx, untrack := mgr.memberConns.track(context.Background(), other)
	defer untrack()
	_ = st.SetMembershipStatus(context.Background(), mid, "inactive")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { mgr.runRemovedMemberSweep(ctx, 10*time.Millisecond); close(done) }()
	defer func() { cancel(); close(f.hold); <-done }()

	deadline := time.Now().Add(5 * time.Second)
	for { // until the first stop holds the sales workspace's lock and is stuck in Stop
		lock := mgr.startLockFor("W-sales")
		if !lock.TryLock() {
			break
		}
		lock.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("control: the sweep never started the stop")
		}
		time.Sleep(time.Millisecond)
	}
	_ = st.SetMembershipStatus(context.Background(), other, "inactive")
	select {
	case <-otherCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a later removal's connection stayed open behind a slow workspace stop")
	}
}
