package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Issue #1199: rotating one membership's internal git token.

// The old token is refused from the very next request, on the git face every verifier
// shares (authorizeGitRepo: smart HTTP, LFS batch/transfer, locks), and the token minted
// at the new epoch works.
func TestRotateGitTokenKillsTheOldTokenAtOnce(t *testing.T) {
	e := newGitTestEnv(t)
	member := e.addMembership(t, "default", "member")
	other := e.addMembership(t, "default", "tenant_admin")
	e.addRepo(t, "default", "shared")
	mgr := e.g.mgr
	ctx := context.Background()
	fetch := "/git/default/shared.git/info/refs?service=git-upload-pack"

	old, _, err := mgr.currentGitToken(ctx, member)
	if err != nil {
		t.Fatal(err)
	}
	if old != mintGitToken(e.signKey, member, 0) {
		t.Fatal("a membership that was never rotated must keep its epoch-0 token")
	}
	otherTok, _, _ := mgr.currentGitToken(ctx, other)
	if w := e.do("GET", fetch, old); w.Code != http.StatusOK {
		t.Fatalf("before rotation: want 200 got %d", w.Code)
	}

	epoch, push, found, err := mgr.rotateGitToken(ctx, member)
	if err != nil || !found || epoch != 1 || push != gitTokenPushDisabled {
		t.Fatalf("rotate = epoch %d push %q found %v err %v, want 1 disabled true nil", epoch, push, found, err)
	}
	if w := e.do("GET", fetch, old); w.Code != http.StatusUnauthorized || e.served {
		t.Fatalf("old token after rotation: want 401 unserved, got %d served=%v", w.Code, e.served)
	}
	fresh, _, err := mgr.currentGitToken(ctx, member)
	if err != nil || fresh == old {
		t.Fatalf("token after rotation = %q (err %v), want a new one", fresh, err)
	}
	if w := e.do("GET", fetch, fresh); w.Code != http.StatusOK || !e.served {
		t.Fatalf("new token: want 200 served, got %d served=%v", w.Code, e.served)
	}
	// Another member of the same tenant is untouched.
	if w := e.do("GET", fetch, otherTok); w.Code != http.StatusOK {
		t.Fatalf("another member's token after the rotation: want 200 got %d", w.Code)
	}
}

// rotateFixture is destroyFixture with a runtime that answers on an Agent endpoint and an
// internal git host configured, plus a recorder in place of the real push.
type pushRecord struct {
	mu                         sync.Mutex
	endpoint, agentTok, gitTok string
	epoch                      int64
	n                          int
	err                        error
}

func rotateFixture(t *testing.T, wsState string) (*store.SQL, *manager, string, store.Tenant, *pushRecord) {
	t.Helper()
	st, mgr, victim, tn := destroyFixture(t, fixedRuntimeFactory{stubRuntime{endpoint: "http://agent.invalid:7731", token: "agent-tok"}})
	mgr.internalGitHost = "af.example"
	mgr.master32 = []byte("master-key-for-rotate-tests-00000")
	if err := st.SetWorkspaceState(context.Background(), "W-1", wsState); err != nil {
		t.Fatal(err)
	}
	rec := &pushRecord{}
	prev := putAgentGitToken
	putAgentGitToken = func(_ context.Context, endpoint, agentToken, gitToken string, epoch int64) error {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		rec.endpoint, rec.agentTok, rec.gitTok, rec.epoch = endpoint, agentToken, gitToken, epoch
		rec.n++
		return rec.err
	}
	t.Cleanup(func() { putAgentGitToken = prev })
	return st, mgr, membershipIDOf(t, st, victim, tn), tn, rec
}

func callRotate(mgr *manager, email, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/admin/rotate-git-token", strings.NewReader(body))
	r.Header.Set("X-Forwarded-Email", email)
	w := httptest.NewRecorder()
	newAdminAPI(mgr).rotateGitToken(w, r)
	return w
}

var errAgentRefused = errors.New("agent answered 404")

const rotateLeaver = `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`

// The running workspace gets the new token without a restart, the memoized start env
// (which still carries the old token) is dropped, and the request and outcome are audited.
func TestRotateGitTokenPushesToTheRunningWorkspace(t *testing.T) {
	ctx := context.Background()
	st, mgr, memID, tn, rec := rotateFixture(t, "running")
	mgr.rts[memID] = cachedRT{rt: stubRuntime{}, ws: store.Workspace{ID: "W-1"}}

	w := callRotate(mgr, "boss@acme.co.jp", rotateLeaver)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"workspace":"updated"`) {
		t.Fatalf("rotate = %d %s, want 200 workspace updated", w.Code, w.Body.String())
	}
	want, _, _ := mgr.currentGitToken(ctx, memID)
	if rec.n != 1 || rec.gitTok != want || rec.epoch != 1 || rec.endpoint != "http://agent.invalid:7731" || rec.agentTok != "agent-tok" {
		t.Fatalf("push = %+v, want one push of %q to the workspace's Agent", rec, want)
	}
	if rec.gitTok == mintGitToken(gitSignKey(mgr.master32), memID, 0) {
		t.Fatal("the push carried the token that was just rotated away")
	}
	if _, ok := mgr.cachedRTFor(memID); ok {
		t.Fatal("the memoized runtime (old token in its env) survived the rotation")
	}
	req, out := auditPair(t, st, tn.ID, "membership.rotate_git_token")
	if req.Target != "leaver-acme-co-jp" || out.HTTPStatus != http.StatusOK || !strings.Contains(out.Detail, "epoch 1, workspace updated") {
		t.Fatalf("audit = %+v / %+v", req, out)
	}
}

func TestRotateGitTokenOutcomes(t *testing.T) {
	t.Run("stopped workspace is not pushed to", func(t *testing.T) {
		_, mgr, _, _, rec := rotateFixture(t, "stopped")
		w := callRotate(mgr, "boss@acme.co.jp", rotateLeaver)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"workspace":"not_running"`) || rec.n != 0 {
			t.Fatalf("rotate = %d %s pushes=%d, want not_running and no push", w.Code, w.Body.String(), rec.n)
		}
	})
	t.Run("an Agent that refuses leaves the rotation in place", func(t *testing.T) {
		st, mgr, memID, _, rec := rotateFixture(t, "running")
		rec.err = errAgentRefused
		w := callRotate(mgr, "boss@acme.co.jp", rotateLeaver)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"workspace":"failed"`) {
			t.Fatalf("rotate = %d %s, want 200 workspace failed", w.Code, w.Body.String())
		}
		if e, _, _ := st.GitTokenEpoch(context.Background(), memID); e != 1 {
			t.Fatalf("epoch after a failed push = %d, want 1 (the old token stays dead)", e)
		}
	})
	t.Run("internal git disabled", func(t *testing.T) {
		_, mgr, _, _, rec := rotateFixture(t, "running")
		mgr.internalGitHost = ""
		w := callRotate(mgr, "boss@acme.co.jp", rotateLeaver)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"workspace":"disabled"`) || rec.n != 0 {
			t.Fatalf("rotate = %d %s pushes=%d, want disabled and no push", w.Code, w.Body.String(), rec.n)
		}
	})
	t.Run("a start in flight is pushed to once it finishes", func(t *testing.T) {
		_, mgr, memID, _, rec := rotateFixture(t, "running")
		prev := gitTokenPushLockWait
		gitTokenPushLockWait = 20 * time.Millisecond
		t.Cleanup(func() { gitTokenPushLockWait = prev })
		lock := mgr.startLockFor("W-1")
		lock.Lock()
		w := callRotate(mgr, "boss@acme.co.jp", rotateLeaver)
		if !strings.Contains(w.Body.String(), `"workspace":"pending"`) {
			lock.Unlock()
			t.Fatalf("rotate during a start = %s, want pending", w.Body.String())
		}
		rec.mu.Lock()
		early := rec.n
		rec.mu.Unlock()
		lock.Unlock()
		if early != 0 {
			t.Fatal("pushed while the start still held the workspace")
		}
		want, _, _ := mgr.currentGitToken(context.Background(), memID)
		deadline := time.Now().Add(5 * time.Second)
		for {
			rec.mu.Lock()
			n, got := rec.n, rec.gitTok
			rec.mu.Unlock()
			if n == 1 && got == want {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("no push after the start finished (pushes %d)", n)
			}
			time.Sleep(5 * time.Millisecond)
		}
		// The background push released the start lock.
		if !lock.TryLock() {
			t.Fatal("the background push kept the workspace's start lock")
		}
		lock.Unlock()
	})
}

// Who may rotate: a tenant_admin of the member's tenant or a super_admin. A tenant_admin
// of another tenant and the member themselves are refused, and nothing moves.
func TestRotateGitTokenAuthorization(t *testing.T) {
	ctx := context.Background()
	st, mgr, memID, _, rec := rotateFixture(t, "running")
	ops, err := st.CreateTenant(ctx, "ops", "Ops")
	if err != nil {
		t.Fatal(err)
	}
	opsAdmin, _ := st.UpsertIdentity(ctx, "opsboss@acme.co.jp", "opsboss-acme-co-jp", "")
	if _, err := st.EnsureMembership(ctx, opsAdmin.ID, ops.ID, "tenant_admin"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, email, body string
		want              int
	}{
		{"another tenant's admin naming the member's tenant", "opsboss@acme.co.jp", rotateLeaver, http.StatusForbidden},
		{"another tenant's admin naming their own tenant", "opsboss@acme.co.jp", `{"tenant_slug":"ops","user_key":"leaver-acme-co-jp"}`, http.StatusNotFound},
		{"the member themselves", "leaver@acme.co.jp", rotateLeaver, http.StatusForbidden},
	} {
		w := callRotate(mgr, c.email, c.body)
		if w.Code != c.want {
			t.Errorf("%s = %d %s, want %d", c.name, w.Code, w.Body.String(), c.want)
		}
	}
	if e, _, _ := st.GitTokenEpoch(ctx, memID); e != 0 || rec.n != 0 {
		t.Fatalf("after refused rotations epoch = %d pushes = %d, want 0 and 0", e, rec.n)
	}
	// A tenant_admin of the member's own tenant (not super_admin) may.
	head, _ := st.UpsertIdentity(ctx, "head@acme.co.jp", "head-acme-co-jp", "")
	sales, _, _ := st.GetTenantBySlug(ctx, "sales")
	if _, err := st.EnsureMembership(ctx, head.ID, sales.ID, "tenant_admin"); err != nil {
		t.Fatal(err)
	}
	if w := callRotate(mgr, "head@acme.co.jp", rotateLeaver); w.Code != http.StatusOK {
		t.Fatalf("own tenant_admin = %d %s, want 200", w.Code, w.Body.String())
	}
}

// The request row is written before the epoch moves; without it nothing moves.
func TestRotateGitTokenRefusesWithoutAnAuditRecord(t *testing.T) {
	st, mgr, memID, _, rec := rotateFixture(t, "running")
	mgr.store = auditFailingStore{st, failEveryAudit}
	wantAuditUnavailable(t, "rotate git token", callRotate(mgr, "boss@acme.co.jp", rotateLeaver))
	if e, _, _ := st.GitTokenEpoch(context.Background(), memID); e != 0 || rec.n != 0 {
		t.Fatalf("epoch = %d pushes = %d after the refusal, want 0 and 0", e, rec.n)
	}
}

// envRecordingFactory hands out a runtime that is stopped until started and remembers the
// env each runtime was built with and which one was started.
type envRecordingFactory struct {
	mu      sync.Mutex
	built   [][]string
	started []string // the env (joined) of each runtime Start was called on
}

type envRecordingRuntime struct {
	stubRuntime
	f   *envRecordingFactory
	env []string
}

func (r *envRecordingRuntime) Start(context.Context) error {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	r.f.started = append(r.f.started, strings.Join(r.env, " "))
	return nil
}

func (f *envRecordingFactory) New(_ runtime.Workspace, _ runtime.SecretKeys, env []string) runtime.Runtime {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.built = append(f.built, env)
	return &envRecordingRuntime{stubRuntime: stubRuntime{state: "stopped"}, f: f, env: env}
}

// A rotation on one CP replica evicts only that replica's memoized runtime. Another
// replica that memoized the workspace before the rotation must still start it with the
// current token, every time, not the env it built back then.
func TestStartOnAReplicaWithAStaleMemoInjectsTheCurrentToken(t *testing.T) {
	ctx := context.Background()
	f := &envRecordingFactory{}
	st, mgrA, victim, tn := destroyFixture(t, f)
	memID := membershipIDOf(t, st, victim, tn)
	master := []byte("master-key-for-rotate-tests-00000")
	mgrB := p3Manager(t, st)
	mgrB.rtFactory = f
	for _, m := range []*manager{mgrA, mgrB} {
		m.internalGitHost = "af.example"
		m.master32 = master
	}
	ws, _, _ := st.GetWorkspaceByMembership(ctx, memID)
	mv, _, _ := st.GetMembershipByID(ctx, memID)

	// Replica B resolves (and memoizes) the stopped workspace at epoch 0.
	resB, aerr := mgrB.buildResolved(ctx, victim, mv)
	if aerr != nil {
		t.Fatal(aerr)
	}
	if !strings.Contains(strings.Join(f.built[len(f.built)-1], " "), "AF_INTERNAL_GIT_EPOCH=0") {
		t.Fatalf("memoized env = %v, want epoch 0", f.built[len(f.built)-1])
	}
	// Replica A rotates.
	if _, push, _, err := mgrA.rotateGitToken(ctx, memID); err != nil || push != gitTokenPushNotRunning {
		t.Fatalf("rotate push=%q err=%v", push, err)
	}
	want, _, _ := mgrA.currentGitToken(ctx, memID)

	// B starts the workspace from its memo, twice (stop in between).
	api := newWorkspaceAPI(mgrB, false)
	for i := 0; i < 2; i++ {
		res, aerr := mgrB.buildResolved(ctx, victim, mv)
		if aerr != nil {
			t.Fatal(aerr)
		}
		if i == 0 && res.rt != resB.rt {
			t.Fatal("precondition: replica B should start from its memoized runtime")
		}
		if aerr := api.ensureWorkspaceStarted(ctx, res); aerr != nil {
			t.Fatalf("start %d: %v", i, aerr)
		}
		f.mu.Lock()
		got := f.started[len(f.started)-1]
		f.mu.Unlock()
		if !strings.Contains(got, "AF_INTERNAL_GIT_TOKEN="+want) || !strings.Contains(got, "AF_INTERNAL_GIT_EPOCH=1") {
			t.Fatalf("start %d on the stale replica injected %q, want the epoch-1 token", i, got)
		}
		if err := st.SetWorkspaceState(ctx, ws.ID, "stopped"); err != nil {
			t.Fatal(err)
		}
	}
}

// A runtime whose epoch matches the live one is started as it is: the rebuild costs a DEK
// unwrap and is only for the stale case.
func TestStartWithACurrentMemoIsNotRebuilt(t *testing.T) {
	ctx := context.Background()
	f := &envRecordingFactory{}
	st, mgr, victim, tn := destroyFixture(t, f)
	mgr.internalGitHost = "af.example"
	mgr.master32 = []byte("master-key-for-rotate-tests-00000")
	mv, _, _ := st.GetMembershipByID(ctx, membershipIDOf(t, st, victim, tn))
	res, aerr := mgr.buildResolved(ctx, victim, mv)
	if aerr != nil {
		t.Fatal(aerr)
	}
	if fresh := mgr.refreshGitTokenForStart(ctx, res, noSecretKeys, nil); fresh != nil {
		t.Fatal("rebuilt a runtime whose token epoch is current")
	}
}

// epochFailingStore fails GitTokenEpoch while fail is set and passes everything else through.
type epochFailingStore struct {
	store.Store
	fail *bool
}

func (s epochFailingStore) GitTokenEpoch(ctx context.Context, id string) (int64, bool, error) {
	if *s.fail {
		return 0, false, errors.New("database is locked")
	}
	return s.Store.GitTokenEpoch(ctx, id)
}

// When the epoch read fails while the env is built, no token is injected. The memo must
// not then claim the current epoch: once the store recovers, the next start has to
// rebuild with the token, or the workspace starts without one at every start.
func TestStartAfterAFailedTokenInjectionRebuildsWithTheToken(t *testing.T) {
	ctx := context.Background()
	f := &envRecordingFactory{}
	st, mgr, victim, tn := destroyFixture(t, f)
	memID := membershipIDOf(t, st, victim, tn)
	mgr.internalGitHost = "af.example"
	mgr.master32 = []byte("master-key-for-rotate-tests-00000")
	if _, _, err := st.BumpGitTokenEpoch(ctx, memID); err != nil {
		t.Fatal(err)
	}
	failing := true
	mgr.store = epochFailingStore{st, &failing}
	mv, _, _ := st.GetMembershipByID(ctx, memID)
	res, aerr := mgr.buildResolved(ctx, victim, mv)
	if aerr != nil {
		t.Fatal(aerr)
	}
	if env := strings.Join(f.built[len(f.built)-1], " "); strings.Contains(env, "AF_INTERNAL_GIT_TOKEN=") {
		t.Fatalf("precondition: the token was injected although its epoch could not be read: %s", env)
	}
	failing = false
	want, _, _ := mgr.currentGitToken(ctx, memID)
	for i := 0; i < 2; i++ {
		res, aerr = mgr.buildResolved(ctx, victim, mv)
		if aerr != nil {
			t.Fatal(aerr)
		}
		if aerr := newWorkspaceAPI(mgr, false).ensureWorkspaceStarted(ctx, res); aerr != nil {
			t.Fatalf("start %d: %v", i, aerr)
		}
		f.mu.Lock()
		got := f.started[len(f.started)-1]
		f.mu.Unlock()
		if !strings.Contains(got, "AF_INTERNAL_GIT_TOKEN="+want) || !strings.Contains(got, "AF_INTERNAL_GIT_EPOCH=1") {
			t.Fatalf("start %d after the store recovered injected %q, want the epoch-1 token", i, got)
		}
		if err := st.SetWorkspaceState(ctx, "W-1", "stopped"); err != nil {
			t.Fatal(err)
		}
	}
}
