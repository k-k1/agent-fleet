package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// --- token unit tests -------------------------------------------------------

func TestGitTokenRoundTrip(t *testing.T) {
	key := gitSignKey([]byte("master-key-for-test-000000000000"))
	tok := mintGitToken(key, "mem-123")
	if got, ok := verifyGitToken(key, tok); !ok || got != "mem-123" {
		t.Fatalf("round trip: got %q ok=%v", got, ok)
	}
	// Determinism: same input → same token (this is what makes injection idempotent).
	if mintGitToken(key, "mem-123") != tok {
		t.Fatal("token not deterministic")
	}
	// Tampered tag is rejected.
	if _, ok := verifyGitToken(key, tok+"x"); ok {
		t.Fatal("accepted tampered tag")
	}
	// A token minted under a different key must not verify (cross-deployment).
	other := gitSignKey([]byte("a-different-master-key-0000000000"))
	if _, ok := verifyGitToken(key, mintGitToken(other, "mem-123")); ok {
		t.Fatal("accepted token signed by a different key")
	}
	// Garbage / wrong prefix.
	for _, bad := range []string{"", "afg_", "afg_nodot", "pat_abc.def", "afg_!!!.tag"} {
		if _, ok := verifyGitToken(key, bad); ok {
			t.Fatalf("accepted malformed token %q", bad)
		}
	}
}

func TestValidRepoName(t *testing.T) {
	ok := []string{"repo", "my-repo", "my_repo.git-ish", "a", "A1", "x.y"}
	bad := []string{"", "..", ".git", "a/b", "../etc", "a b", "a/../b", ".", "-lead", "/abs"}
	for _, n := range ok {
		if !validRepoName(n) {
			t.Errorf("expected %q valid", n)
		}
	}
	for _, n := range bad {
		if validRepoName(n) {
			t.Errorf("expected %q invalid", n)
		}
	}
}

func TestIsReceivePackAndCanPush(t *testing.T) {
	mk := func(method, target string) *http.Request {
		return httptest.NewRequest(method, target, nil)
	}
	if !isReceivePack(mk("GET", "/git/t/r.git/info/refs?service=git-receive-pack")) {
		t.Error("push discovery not detected")
	}
	if !isReceivePack(mk("POST", "/git/t/r.git/git-receive-pack")) {
		t.Error("push not detected")
	}
	if isReceivePack(mk("GET", "/git/t/r.git/info/refs?service=git-upload-pack")) {
		t.Error("fetch misdetected as push")
	}
	if isReceivePack(mk("POST", "/git/t/r.git/git-upload-pack")) {
		t.Error("fetch pack misdetected as push")
	}
	if !canPush("member") || !canPush("tenant_admin") {
		t.Error("member/tenant_admin should push")
	}
	if canPush("viewer") || canPush("") {
		t.Error("viewer/empty must not push")
	}
}

// --- smart-HTTP isolation tests --------------------------------------------

// gitTestEnv wires an in-memory store with two tenants and returns a gitServerAPI
// plus helpers for minting tokens and issuing requests, with the CGI backend stubbed.
type gitTestEnv struct {
	g        gitServerAPI
	st       *store.SQL
	signKey  []byte
	served   bool
	servedTo string // tenantRoot the authorized request resolved to
}

func newGitTestEnv(t *testing.T) *gitTestEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	master := []byte("master-key-for-git-http-tests-000")
	env := &gitTestEnv{
		st:      st,
		signKey: gitSignKey(master),
		g: newGitServerAPI(&manager{
			store:    st,
			master32: master,
			dataRoot: t.TempDir(),
		}, ""),
	}
	// Stub the CGI backend so the authorized path is observable without git.
	prev := gitBackendServe
	gitBackendServe = func(w http.ResponseWriter, r *http.Request, slug, tenantRoot, remoteUser string) {
		env.served = true
		env.servedTo = tenantRoot
		w.WriteHeader(http.StatusOK)
	}
	t.Cleanup(func() { gitBackendServe = prev })
	return env
}

// addMembership inserts an identity + tenant + membership (raw, so any role incl.
// a read-only "viewer" can be exercised) and returns the membership id.
func (e *gitTestEnv) addMembership(t *testing.T, tenantSlug, role string) string {
	t.Helper()
	ctx := context.Background()
	tn, err := e.st.CreateTenant(ctx, tenantSlug, tenantSlug)
	if err != nil {
		// tenant may already exist from a prior membership in the same tenant
		got, ok, gerr := e.st.GetTenantBySlug(ctx, tenantSlug)
		if gerr != nil || !ok {
			t.Fatalf("tenant %s: %v / %v", tenantSlug, err, gerr)
		}
		tn = got
	}
	ident, err := e.st.UpsertIdentity(ctx, tenantSlug+"-"+role+"@x", "key-"+tenantSlug+"-"+role, "")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	mid := store.NewID()
	if _, err := e.st.DB().ExecContext(ctx,
		`INSERT INTO membership(id, identity_id, tenant_id, role, status, created_at) VALUES(?,?,?,?, 'active', ?)`,
		mid, ident.ID, tn.ID, role, store.NowTS()); err != nil {
		t.Fatalf("membership: %v", err)
	}
	return mid
}

func (e *gitTestEnv) addRepo(t *testing.T, tenantSlug, name string) {
	t.Helper()
	ctx := context.Background()
	tn, ok, err := e.st.GetTenantBySlug(ctx, tenantSlug)
	if err != nil || !ok {
		t.Fatalf("tenant lookup %s: %v ok=%v", tenantSlug, err, ok)
	}
	if err := e.st.CreateGitRepo(ctx, store.GitRepo{
		ID: store.NewID(), TenantID: tn.ID, Name: name, DefaultBranch: "main", CreatedAt: store.NowTS(),
	}); err != nil {
		t.Fatalf("create repo: %v", err)
	}
}

func (e *gitTestEnv) do(method, path, token string) *httptest.ResponseRecorder {
	e.served, e.servedTo = false, ""
	r := httptest.NewRequest(method, path, nil)
	if token != "" {
		r.SetBasicAuth("x-access-token", token)
	}
	w := httptest.NewRecorder()
	e.g.gitHTTP(w, r)
	return w
}

func TestGitHTTPAuthAndIsolation(t *testing.T) {
	e := newGitTestEnv(t)
	memberDefault := e.addMembership(t, "default", "member")
	e.addRepo(t, "default", "shared")
	// A second tenant with its own repo, and a member of it.
	memberSec := e.addMembership(t, "security", "member")
	e.addRepo(t, "security", "secret")

	tokDefault := mintGitToken(e.signKey, memberDefault)
	fetch := "/git/default/shared.git/info/refs?service=git-upload-pack"

	// No credentials → 401.
	if w := e.do("GET", fetch, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("no auth: want 401 got %d", w.Code)
	}
	// Garbage token → 401.
	if w := e.do("GET", fetch, "afg_bogus.tag"); w.Code != http.StatusUnauthorized {
		t.Fatalf("bad token: want 401 got %d", w.Code)
	}
	// Valid member fetching own tenant's repo → served, and scoped to that tenant.
	w := e.do("GET", fetch, tokDefault)
	if w.Code != http.StatusOK || !e.served {
		t.Fatalf("authorized fetch: code=%d served=%v", w.Code, e.served)
	}
	if want := filepath.Join(e.g.dataRoot, "git", "default"); e.servedTo != want {
		t.Fatalf("served to %q, want tenant root %q", e.servedTo, want)
	}

	// CROSS-TENANT: default's token against the security tenant's URL → 403, not served.
	w = e.do("GET", "/git/security/secret.git/info/refs?service=git-upload-pack", tokDefault)
	if w.Code != http.StatusForbidden || e.served {
		t.Fatalf("cross-tenant: want 403 unserved, got code=%d served=%v", w.Code, e.served)
	}
	// And the reverse: the security member cannot reach default's repo.
	w = e.do("GET", fetch, mintGitToken(e.signKey, memberSec))
	if w.Code != http.StatusForbidden || e.served {
		t.Fatalf("cross-tenant reverse: want 403, got %d served=%v", w.Code, e.served)
	}

	// Unknown repo in own tenant → 404 (ledger is the source of truth).
	w = e.do("GET", "/git/default/nope.git/info/refs?service=git-upload-pack", tokDefault)
	if w.Code != http.StatusNotFound || e.served {
		t.Fatalf("unknown repo: want 404, got %d served=%v", w.Code, e.served)
	}
}

func TestGitHTTPPushRoleGate(t *testing.T) {
	e := newGitTestEnv(t)
	viewer := e.addMembership(t, "default", "viewer")
	member := e.addMembership(t, "default", "member")
	e.addRepo(t, "default", "shared")

	push := "/git/default/shared.git/git-receive-pack"
	// viewer may read but not push.
	if w := e.do("GET", "/git/default/shared.git/info/refs?service=git-upload-pack", mintGitToken(e.signKey, viewer)); w.Code != http.StatusOK {
		t.Fatalf("viewer read: want 200 got %d", w.Code)
	}
	if w := e.do("POST", push, mintGitToken(e.signKey, viewer)); w.Code != http.StatusForbidden || e.served {
		t.Fatalf("viewer push: want 403 unserved, got %d served=%v", w.Code, e.served)
	}
	// member may push.
	if w := e.do("POST", push, mintGitToken(e.signKey, member)); w.Code != http.StatusOK || !e.served {
		t.Fatalf("member push: want 200 served, got %d served=%v", w.Code, e.served)
	}
}

func TestGitHTTPRevokedMembership(t *testing.T) {
	e := newGitTestEnv(t)
	member := e.addMembership(t, "default", "member")
	e.addRepo(t, "default", "shared")
	tok := mintGitToken(e.signKey, member)
	fetch := "/git/default/shared.git/info/refs?service=git-upload-pack"
	if w := e.do("GET", fetch, tok); w.Code != http.StatusOK {
		t.Fatalf("pre-revoke: want 200 got %d", w.Code)
	}
	// Deactivate the membership: GetMembershipByID filters status='active', so the
	// same (deterministic) token stops working with no token table to revoke.
	if _, err := e.st.DB().ExecContext(context.Background(),
		`UPDATE membership SET status='disabled' WHERE id=?`, member); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if w := e.do("GET", fetch, tok); w.Code != http.StatusUnauthorized || e.served {
		t.Fatalf("post-revoke: want 401 unserved, got %d served=%v", w.Code, e.served)
	}
}
