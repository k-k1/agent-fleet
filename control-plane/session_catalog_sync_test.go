package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// syncBenchAgent stands in for the Workspace Agent of the member behind issue #2074: 1,200
// sessions (most archived) and a slow /repos. The delays are the measured 0.6 s and 5.1 s
// scaled down by five so the suite stays quick.
type syncBenchAgent struct {
	srv          *httptest.Server
	catalogHits  atomic.Int32
	reposHits    atomic.Int32
	catalogDelay time.Duration
	reposDelay   time.Duration
	reposFail    atomic.Bool
	// catalogEntered receives once per /sessions/catalog request, when it starts.
	catalogEntered chan struct{}
	mu             sync.Mutex
	sessions       []map[string]any
	repos          []map[string]any
	// delta makes the fake speak the catalog delta protocol the Agent implements
	// (workspace/agent/internal/sessionx/catalog_delta.go); off, it behaves like an Agent that
	// predates it: whole catalog, no cursor.
	delta     bool
	epoch     string
	rev       uint64
	revs      map[string]uint64
	tombs     map[string]uint64
	sinceSeen []string // the `since` of each catalog request, in order
	bytesSent atomic.Int64
}

func newSyncBenchAgent(t *testing.T, n int) *syncBenchAgent {
	t.Helper()
	a := &syncBenchAgent{catalogDelay: 120 * time.Millisecond, reposDelay: 1020 * time.Millisecond,
		catalogEntered: make(chan struct{}, 64), epoch: "e0", revs: map[string]uint64{}, tombs: map[string]uint64{}}
	for i := 0; i < n; i++ {
		a.sessions = append(a.sessions, map[string]any{"name": fmt.Sprintf("s-%04d", i), "kind": "claude",
			"dir": "/home/dev/repos/app", "repo": "app", "workingCopyId": fmt.Sprintf("wc-%d", i%7),
			"title": fmt.Sprintf("Session %d", i), "archived": i >= 56,
			"createdAt": fmt.Sprintf("2026-10-01T00:%02d:%02dZ", (i/60)%60, i%60)})
	}
	for i := 0; i < 7; i++ {
		a.repos = append(a.repos, map[string]any{"name": fmt.Sprintf("app%d", i), "workingCopyId": fmt.Sprintf("wc-%d", i)})
	}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sessions/catalog":
			a.catalogHits.Add(1)
			select {
			case a.catalogEntered <- struct{}{}:
			default:
			}
			time.Sleep(a.catalogDelay)
			a.mu.Lock()
			body := map[string]any{"sessions": a.sessions}
			if a.delta {
				body = a.deltaBody(r.URL.Query().Get("since"))
			}
			raw, _ := json.Marshal(body)
			a.bytesSent.Add(int64(len(raw)))
			_, _ = w.Write(raw)
			a.mu.Unlock()
		case "/repos":
			a.reposHits.Add(1)
			time.Sleep(a.reposDelay)
			if a.reposFail.Load() {
				http.Error(w, "git failed", http.StatusInternalServerError)
				return
			}
			a.mu.Lock()
			_ = json.NewEncoder(w).Encode(map[string]any{"repos": a.repos})
			a.mu.Unlock()
		case "/sessions/s-0001/handoff-context":
			_ = json.NewEncoder(w).Encode(map[string]any{"repo": "app", "vcs": "git", "branch": "main", "headSha": "abcdef1"})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "not_found"}})
		}
	}))
	t.Cleanup(a.srv.Close)
	return a
}

// deltaBody answers one catalog request the way the Agent does. Call with a.mu held.
func (a *syncBenchAgent) deltaBody(since string) map[string]any {
	a.sinceSeen = append(a.sinceSeen, since)
	cursor := fmt.Sprintf("%s.%d", a.epoch, a.rev)
	prefix := a.epoch + "."
	if !strings.HasPrefix(since, prefix) {
		return map[string]any{"sessions": a.sessions, "cursor": cursor}
	}
	var rev uint64
	if _, err := fmt.Sscanf(strings.TrimPrefix(since, prefix), "%d", &rev); err != nil || rev > a.rev {
		return map[string]any{"sessions": a.sessions, "cursor": cursor}
	}
	changed := []map[string]any{}
	for _, s := range a.sessions {
		if a.revs[s["name"].(string)] > rev {
			changed = append(changed, s)
		}
	}
	deleted := []string{}
	for n, r := range a.tombs {
		if r > rev {
			deleted = append(deleted, n)
		}
	}
	return map[string]any{"sessions": changed, "cursor": cursor, "delta": true, "deleted": deleted}
}

// change edits one session and gives it the next revision.
func (a *syncBenchAgent) change(name string, edit func(map[string]any)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range a.sessions {
		if s["name"] == name {
			edit(s)
			a.rev++
			a.revs[name] = a.rev
			return
		}
	}
}

func (a *syncBenchAgent) remove(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, s := range a.sessions {
		if s["name"] == name {
			a.sessions = append(a.sessions[:i:i], a.sessions[i+1:]...)
			a.rev++
			a.tombs[name] = a.rev
			delete(a.revs, name)
			return
		}
	}
}

// restart is an Agent process restart: a new epoch, the counter back at zero.
func (a *syncBenchAgent) restart() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.epoch, a.rev, a.revs, a.tombs = fmt.Sprintf("e%d", time.Now().UnixNano()), 0, map[string]uint64{}, map[string]uint64{}
}

type syncBenchFixture struct {
	st        *store.SQL
	mgr       *manager
	api       sessionShareAPI
	res       *resolved
	ownerID   store.Identity
	ownerView store.MembershipView
	share     store.SessionShare
	agent     *syncBenchAgent
}

func newSyncBenchFixture(t *testing.T, n int) syncBenchFixture {
	t.Helper()
	ctx := context.Background()
	st, err := openMigratedSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	oi, _ := st.UpsertIdentity(ctx, "owner-bench@example.com", "owner-bench", "")
	ri, _ := st.UpsertIdentity(ctx, "peer-bench@example.com", "peer-bench", "")
	owner, _ := st.EnsureMembership(ctx, oi.ID, tenant.ID, "member")
	peer, _ := st.EnsureMembership(ctx, ri.ID, tenant.ID, "member")
	views, _ := st.ListMemberships(ctx, oi.ID)
	ws := store.Workspace{ID: store.NewID(), TenantID: tenant.ID, MembershipID: owner.ID, ContainerName: "c", Network: "n",
		DataDir: "d", AgentPort: "1", AgentToken: "tok", State: "running", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	share := store.SessionShare{ID: store.NewID(), TenantID: tenant.ID, OwnerMembershipID: owner.ID,
		RecipientMembershipID: peer.ID, ScopeType: "session", ScopeKey: "s-0001", Permission: "ro",
		CreatedAt: store.NowTS(), UpdatedAt: store.NowTS()}
	if err := st.PutSessionShare(ctx, share); err != nil {
		t.Fatal(err)
	}
	agent := newSyncBenchAgent(t, n)
	rt := stubRuntime{endpoint: agent.srv.URL, token: "tok"}
	mgr := &manager{store: st, rts: map[string]cachedRT{owner.ID: {rt: rt, ws: ws}}}
	return syncBenchFixture{st: st, mgr: mgr, api: newSessionShareAPI(mgr), ownerID: oi, ownerView: views[0], share: share,
		agent: agent, res: &resolved{ident: oi, mv: views[0], ws: ws, rt: rt}}
}

// Two shared views asking for the owner's inventory at the same moment must cost the
// owner's Agent one round trip, not one each: recipients' tabs poll, and every extra sync
// was another 5 s of git in the owner's Workspace.
func TestCatalogSyncCoalescesConcurrentCallers(t *testing.T) {
	f := newSyncBenchFixture(t, 20)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- f.api.syncCatalog(context.Background(), f.res)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if c, r := f.agent.catalogHits.Load(), f.agent.reposHits.Load(); c != 1 || r != 1 {
		t.Fatalf("two concurrent syncs hit the Agent catalog=%d repos=%d times, want 1 each", c, r)
	}
}

// An unshare issued while the owner's inventory is being synced must not wait for the
// owner's Agent: the share lock covers only the database write now. Measured with the
// shape of the member behind #2074 (1,200 sessions, /repos five times slower than the
// catalog).
func TestShareDeleteDoesNotWaitForCatalogSync(t *testing.T) {
	f := newSyncBenchFixture(t, 1200)
	ctx := context.Background()
	// Seed the catalog the way the first sync after a CP start does.
	start := time.Now()
	stmts := f.st.StatementCount()
	if err := f.api.syncCatalog(ctx, f.res); err != nil {
		t.Fatal(err)
	}
	t.Logf("first sync: %v, %d statements", time.Since(start), f.st.StatementCount()-stmts)

	start = time.Now()
	stmts = f.st.StatementCount()
	if err := f.api.syncCatalog(ctx, f.res); err != nil {
		t.Fatal(err)
	}
	t.Logf("unchanged sync: %v, %d statements", time.Since(start), f.st.StatementCount()-stmts)

	for len(f.agent.catalogEntered) > 0 {
		<-f.agent.catalogEntered
	}
	syncDone := make(chan time.Duration, 1)
	go func() {
		s := time.Now()
		// A forced refresh, as put and the handoff create ask for.
		if _, err := f.mgr.syncOwnerCatalog(ctx, f.res, catalogSyncNeed{exact: true, liveRepos: true}); err != nil {
			t.Error(err)
		}
		syncDone <- time.Since(s)
	}()
	<-f.agent.catalogEntered // the sync is now waiting on the owner's Agent

	req := httptest.NewRequest(http.MethodDelete, "/api/session-shares/"+f.share.ID, nil)
	req.SetPathValue("id", f.share.ID)
	rec := httptest.NewRecorder()
	start = time.Now()
	f.api.delete(rec, req, f.ownerID, f.ownerView)
	took := time.Since(start)
	t.Logf("share delete issued during a sync: %v (status %d)", took, rec.Code)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	if took > 300*time.Millisecond {
		t.Fatalf("share delete waited %v for a catalog sync in flight", took)
	}
	t.Logf("the sync it ran beside: %v", <-syncDone)
}

func (a *syncBenchAgent) set(sessions, repos []map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if sessions != nil {
		a.sessions = sessions
	}
	if repos != nil {
		a.repos = repos
	}
}

func (f syncBenchFixture) expireRepos() {
	cs := f.mgr.catalogSyncer()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if o := cs.owners[f.res.mv.MembershipID]; o != nil {
		o.reposAt = time.Time{}
	}
}

func (f syncBenchFixture) row(t *testing.T, name string) (store.SharedSessionCatalog, bool) {
	t.Helper()
	rows, err := f.st.ListSharedSessionCatalogByOwner(context.Background(), f.res.mv.MembershipID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Name == name {
			return r, true
		}
	}
	return store.SharedSessionCatalog{}, false
}

// A caller that needs the inventory as of now (share put, handoff create, approval) must
// not be handed a sync that read the Agent before it asked: it queues one more sync, while
// throttled readers keep joining the one in flight.
func TestExactCatalogSyncDoesNotReuseAnEarlierFlight(t *testing.T) {
	f := newSyncBenchFixture(t, 3)
	f.agent.catalogDelay, f.agent.reposDelay = 200*time.Millisecond, 0
	ctx := context.Background()
	first := make(chan error, 1)
	go func() { first <- f.api.syncCatalog(ctx, f.res) }()
	<-f.agent.catalogEntered
	// The owner creates a session while that sync is already waiting on the Agent.
	f.agent.mu.Lock()
	f.agent.sessions = append(f.agent.sessions, map[string]any{"name": "s-late", "kind": "codex", "workingCopyId": "wc-0"})
	f.agent.mu.Unlock()
	joined := make(chan error, 1)
	go func() { joined <- f.api.syncCatalog(ctx, f.res) }()
	if _, err := f.mgr.syncOwnerCatalog(ctx, f.res, catalogSyncNeed{exact: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.row(t, "s-late"); !ok {
		t.Fatal("an exact sync returned a result read before it was asked for")
	}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-joined; err != nil {
		t.Fatal(err)
	}
	if got := f.agent.catalogHits.Load(); got != 2 {
		t.Fatalf("catalog hits=%d, want 2 (the running sync, joined once, and one exact sync)", got)
	}
}

// /repos is the slow half of a sync (git per working copy). It is reused within
// shareReposTTL, read again as soon as a session sits in a working copy it does not know
// (so a repo share reaches a new worktree at once), read live for a repo-scoped put, and
// only a live read prunes the rule of a working copy that is gone.
func TestCatalogSyncReusesReposUntilItMissesAWorkingCopy(t *testing.T) {
	f := newSyncBenchFixture(t, 3)
	f.agent.catalogDelay, f.agent.reposDelay = 0, 0
	ctx := context.Background()
	sync := func() {
		t.Helper()
		if err := f.api.syncCatalog(ctx, f.res); err != nil {
			t.Fatal(err)
		}
	}
	sync()
	sync()
	if got := f.agent.reposHits.Load(); got != 1 {
		t.Fatalf("/repos hits=%d after two syncs within the TTL, want 1", got)
	}

	f.agent.mu.Lock()
	f.agent.sessions = append(f.agent.sessions, map[string]any{"name": "s-wt", "kind": "codex", "workingCopyId": "wc-new"})
	f.agent.repos = append(f.agent.repos, map[string]any{"name": "app0@feat", "workingCopyId": "wc-new", "worktree": true, "parent": "app0", "branch": "feat"})
	f.agent.mu.Unlock()
	sync()
	if got := f.agent.reposHits.Load(); got != 2 {
		t.Fatalf("/repos hits=%d, want a live read for the unknown working copy", got)
	}
	if r, _ := f.row(t, "s-wt"); r.ParentWorkingCopyID != "wc-0" || !r.Worktree || r.Branch != "feat" {
		t.Fatalf("new worktree row=%+v", r)
	}

	body := `{"recipientUserKey":"peer-bench","scope":{"type":"worktree","key":"wc-new"},"permission":"ro"}`
	req := httptest.NewRequest(http.MethodPut, "/api/session-shares", strings.NewReader(body))
	rec := httptest.NewRecorder()
	f.api.put(rec, req, f.res)
	if rec.Code != http.StatusCreated {
		t.Fatalf("put status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := f.agent.reposHits.Load(); got != 3 {
		t.Fatalf("/repos hits=%d, want a live read for a worktree-scoped put", got)
	}

	// The worktree is removed, and another CP replica has stored a rule for a working copy
	// this replica's cached inventory has never seen. A sync on the cached inventory must
	// prune neither...
	f.agent.set(f.agent.sessions[:3], f.agent.repos[:7])
	if err := f.st.PutSessionShare(ctx, store.SessionShare{ID: store.NewID(), TenantID: f.share.TenantID,
		OwnerMembershipID: f.share.OwnerMembershipID, RecipientMembershipID: f.share.RecipientMembershipID,
		ScopeType: "repo", ScopeKey: "wc-elsewhere", Permission: "ro", CreatedAt: store.NowTS(), UpdatedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	sync()
	if got := f.agent.reposHits.Load(); got != 3 {
		t.Fatalf("/repos hits=%d, want the cached read", got)
	}
	shares, _ := f.st.ListSessionSharesByOwner(ctx, f.res.mv.MembershipID)
	if len(shares) != 3 {
		t.Fatalf("a cached /repos pruned rules: %+v", shares)
	}
	// ...a live one does.
	f.expireRepos()
	sync()
	shares, _ = f.st.ListSessionSharesByOwner(ctx, f.res.mv.MembershipID)
	if len(shares) != 1 || shares[0].ScopeType != "session" {
		t.Fatalf("the live read did not prune the removed worktree's rule: %+v", shares)
	}
}

// When /repos fails and nothing is cached (a CP restart), the sync must keep each row's
// stored worktree parent: writing it empty takes the worktree's sessions out of a repo share
// until /repos answers again.
func TestCatalogSyncKeepsStoredParentWhenReposFails(t *testing.T) {
	f := newSyncBenchFixture(t, 0)
	f.agent.catalogDelay, f.agent.reposDelay = 0, 0
	f.agent.set([]map[string]any{{"name": "s-wt", "kind": "codex", "workingCopyId": "wc-wt"}},
		[]map[string]any{{"name": "app", "workingCopyId": "wc-base"}, {"name": "app@feat", "workingCopyId": "wc-wt", "worktree": true, "parent": "app"}})
	ctx := context.Background()
	if err := f.api.syncCatalog(ctx, f.res); err != nil {
		t.Fatal(err)
	}
	if r, _ := f.row(t, "s-wt"); r.ParentWorkingCopyID != "wc-base" {
		t.Fatalf("seed row=%+v", r)
	}
	f.mgr.catalogSync = nil // a restarted CP has no cached inventory
	f.agent.reposFail.Store(true)
	if err := f.api.syncCatalog(ctx, f.res); err != nil {
		t.Fatal(err)
	}
	if got := f.agent.reposHits.Load(); got != 2 {
		t.Fatalf("/repos hits=%d, want the failed live read", got)
	}
	if r, _ := f.row(t, "s-wt"); r.ParentWorkingCopyID != "wc-base" || !r.Worktree {
		t.Fatalf("a failed /repos read cleared the stored parent: %+v", r)
	}
}

// The handover modal's recipient list (GET /api/sessions/{name}/handoff-recipients) syncs
// the owner's catalog whenever the throttle has lapsed. It used to pay for /repos every
// time; now a lapsed throttle costs the catalog read alone while the /repos cache is good.
func TestHandoffRecipientsDoNotWaitForReposWithinItsTTL(t *testing.T) {
	f := newSyncBenchFixture(t, 1200)
	api := newSessionHandoffAPI(f.mgr, f.api, false)
	get := func() time.Duration {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/s-0001/handoff-recipients", nil)
		req.SetPathValue("name", "s-0001")
		rec := httptest.NewRecorder()
		start := time.Now()
		api.recipients(rec, req, f.res)
		took := time.Since(start)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "peer-bench") {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		return took
	}
	t.Logf("handoff recipients, cold: %v", get())
	f.api.invalidateCatalog(f.res.mv.MembershipID) // the throttle lapses
	repos := f.agent.reposHits.Load()
	warm := get()
	t.Logf("handoff recipients, throttle lapsed: %v", warm)
	// Asserted on the call, not the wall time: the timing is logged for the PR, but a busy
	// host or -race makes decoding 1,200 rows slow enough to cross any fixed bound.
	if f.agent.reposHits.Load() != repos {
		t.Fatalf("handoff recipients read /repos again within its TTL (took %v)", warm)
	}
	if f.agent.catalogHits.Load() != 2 {
		t.Fatalf("catalog hits=%d, want a fresh catalog read once the throttle lapsed", f.agent.catalogHits.Load())
	}
}

// put syncs outside the share lock, so another sync can apply a newer inventory before put
// takes the lock. A working copy that newer sync saw vanish (and pruned the rules of) must
// not get a fresh rule from the inventory put read earlier.
func TestSharePutRechecksTheInventoryAppliedAfterItsSync(t *testing.T) {
	f := newSyncBenchFixture(t, 3)
	f.agent.catalogDelay, f.agent.reposDelay = 0, 0
	ctx := context.Background()
	paused, resume := make(chan struct{}), make(chan struct{})
	sharePutSyncedHook = func() {
		close(paused)
		<-resume
	}
	t.Cleanup(func() { sharePutSyncedHook = nil })

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		body := `{"recipientUserKey":"peer-bench","scope":{"type":"repo","key":"wc-0"},"permission":"ro"}`
		rec := httptest.NewRecorder()
		f.api.put(rec, httptest.NewRequest(http.MethodPut, "/api/session-shares", strings.NewReader(body)), f.res)
		done <- rec
	}()
	<-paused // put's own sync saw wc-0
	f.agent.set([]map[string]any{}, []map[string]any{})
	if _, err := f.mgr.syncOwnerCatalog(ctx, f.res, catalogSyncNeed{exact: true, liveRepos: true}); err != nil {
		t.Fatal(err)
	}
	close(resume)
	rec := <-done
	if rec.Code != http.StatusNotFound {
		t.Fatalf("put on a working copy a later sync saw vanish: status=%d body=%s", rec.Code, rec.Body.String())
	}
	shares, _ := f.st.ListSessionSharesByOwner(ctx, f.res.mv.MembershipID)
	for _, s := range shares {
		if s.ScopeType == "repo" {
			t.Fatalf("rule created for a vanished working copy: %+v", s)
		}
	}
}

type panicStateRuntime struct{ stubRuntime }

func (panicStateRuntime) State(context.Context) string { panic("boom") }

// A panic inside a sync must fail that sync, not wedge the owner's queue: every later sync
// would otherwise wait on a flight that never finishes.
func TestCatalogSyncSurvivesAPanickingFlight(t *testing.T) {
	f := newSyncBenchFixture(t, 3)
	f.agent.catalogDelay, f.agent.reposDelay = 0, 0
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bad := *f.res
	bad.rt = panicStateRuntime{}
	if _, err := f.mgr.syncOwnerCatalog(ctx, &bad, catalogSyncNeed{}); err == nil || ctx.Err() != nil {
		t.Fatalf("panicking sync: err=%v ctx=%v", err, ctx.Err())
	}
	if _, err := f.mgr.syncOwnerCatalog(ctx, f.res, catalogSyncNeed{exact: true}); err != nil {
		t.Fatalf("the next sync after a panic: %v", err)
	}
	if _, ok := f.row(t, "s-0001"); !ok {
		t.Fatal("the next sync did not run")
	}
}

func TestCatalogSyncerEvictsIdleOwners(t *testing.T) {
	cs := &catalogSyncer{owners: map[string]*ownerCatalogSync{}}
	busy := &catalogFlight{done: make(chan struct{})}
	cs.owners["busy"] = &ownerCatalogSync{cur: busy}
	cs.owners["fresh"] = &ownerCatalogSync{reposAt: time.Now()}
	cs.owners["idle"] = &ownerCatalogSync{reposAt: time.Now().Add(-2 * shareReposTTL)}
	cs.evictIdle()
	if _, ok := cs.owners["idle"]; ok {
		t.Fatal("an idle owner with an expired cache was kept")
	}
	if len(cs.owners) != 2 {
		t.Fatalf("owners=%v, want busy and fresh kept", cs.owners)
	}
}
