package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// lfsEnv wires a store + gitServerAPI with a default-tenant member and a repo
// "shared", plus a valid git token, for exercising the LFS handlers directly.
type lfsEnv struct {
	g     gitServerAPI
	st    *sqlStore
	token string
	memID string
}

func newLFSEnv(t *testing.T) *lfsEnv {
	t.Helper()
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	dflt, _ := st.EnsureDefaultTenant(ctx)
	ident, _ := st.UpsertIdentity(ctx, "u@x", "u-x", "")
	mem, _ := st.EnsureMembership(ctx, ident.ID, dflt.ID, "member")
	if err := st.CreateGitRepo(ctx, GitRepo{ID: newID(), TenantID: dflt.ID, Name: "shared", DefaultBranch: "main", CreatedAt: nowTS()}); err != nil {
		t.Fatalf("repo: %v", err)
	}
	master := []byte("master-key-lfs-tests-00000000000000")
	return &lfsEnv{
		st:    st,
		memID: mem.ID,
		token: mintGitToken(gitSignKey(master), mem.ID),
		g: newGitServerAPI(&manager{store: st, master32: master, dataRoot: t.TempDir()},
			"https://fleet.example.com"),
	}
}

func (e *lfsEnv) setLFSCap(t *testing.T, bytes int64) {
	t.Helper()
	dflt, _, _ := e.st.GetTenantBySlug(context.Background(), "default")
	lj, _ := json.Marshal(tenantLimits{MaxLFSBytes: bytes})
	if err := e.st.SetTenantLimits(context.Background(), dflt.ID, string(lj)); err != nil {
		t.Fatalf("set cap: %v", err)
	}
}

func oidOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// req builds an authenticated LFS request with the given path values set (the mux
// would normally populate them).
func (e *lfsEnv) req(method, path string, body []byte, pv map[string]string) *http.Request {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.SetBasicAuth("x-access-token", e.token)
	for k, v := range pv {
		r.SetPathValue(k, v)
	}
	return r
}

func (e *lfsEnv) batch(t *testing.T, op string, objs []map[string]any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"operation": op, "transfers": []string{"basic"}, "objects": objs})
	r := e.req("POST", "/git/default/shared.git/info/lfs/objects/batch", body,
		map[string]string{"slug": "default", "repo": "shared.git"})
	w := httptest.NewRecorder()
	e.g.lfsBatch(w, r)
	if w.Code != 200 {
		t.Fatalf("batch %s: want 200 got %d (%s)", op, w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("batch decode: %v", err)
	}
	return out
}

func (e *lfsEnv) upload(oid string, data []byte) *httptest.ResponseRecorder {
	r := e.req("PUT", "/git/default/shared.git/info/lfs/objects/"+oid, data,
		map[string]string{"slug": "default", "repo": "shared.git", "oid": oid})
	w := httptest.NewRecorder()
	e.g.lfsUpload(w, r)
	return w
}

func (e *lfsEnv) download(oid string) *httptest.ResponseRecorder {
	r := e.req("GET", "/git/default/shared.git/info/lfs/objects/"+oid, nil,
		map[string]string{"slug": "default", "repo": "shared.git", "oid": oid})
	w := httptest.NewRecorder()
	e.g.lfsDownload(w, r)
	return w
}

func TestLFSRoundTrip(t *testing.T) {
	e := newLFSEnv(t)
	data := []byte("hello large file\n")
	oid := oidOf(data)

	// Download of a missing object → batch returns an error entry, GET → 404.
	dl := e.batch(t, "download", []map[string]any{{"oid": oid, "size": len(data)}})
	obj0 := dl["objects"].([]any)[0].(map[string]any)
	if _, hasAction := obj0["actions"]; hasAction || obj0["error"] == nil {
		t.Fatalf("missing object should have error, got %v", obj0)
	}
	if w := e.download(oid); w.Code != 404 {
		t.Fatalf("download missing: want 404 got %d", w.Code)
	}

	// Upload batch → an upload action.
	ub := e.batch(t, "upload", []map[string]any{{"oid": oid, "size": len(data)}})
	uobj := ub["objects"].([]any)[0].(map[string]any)
	acts, ok := uobj["actions"].(map[string]any)
	if !ok || acts["upload"] == nil {
		t.Fatalf("upload batch should offer upload action, got %v", uobj)
	}
	if href := acts["upload"].(map[string]any)["href"].(string); !strings.HasSuffix(href, "/info/lfs/objects/"+oid) {
		t.Fatalf("unexpected upload href: %s", href)
	}

	// PUT the bytes, then GET them back.
	if w := e.upload(oid, data); w.Code != 200 {
		t.Fatalf("upload: want 200 got %d (%s)", w.Code, w.Body.String())
	}
	if w := e.download(oid); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatalf("download: code=%d body=%q", w.Code, w.Body.String())
	}
	// Ledger reflects the bytes.
	if n, _ := e.st.TenantLFSBytes(context.Background(), e.tenantID()); n != int64(len(data)) {
		t.Fatalf("ledger bytes = %d want %d", n, len(data))
	}

	// A second upload batch for the same oid → no action (dedup, already stored).
	ub2 := e.batch(t, "upload", []map[string]any{{"oid": oid, "size": len(data)}})
	if _, hasAction := ub2["objects"].([]any)[0].(map[string]any)["actions"]; hasAction {
		t.Fatal("already-stored object should not be offered for upload")
	}
	// Download batch now offers a download action.
	dl2 := e.batch(t, "download", []map[string]any{{"oid": oid, "size": len(data)}})
	if _, ok := dl2["objects"].([]any)[0].(map[string]any)["actions"]; !ok {
		t.Fatal("stored object should offer download action")
	}
}

func (e *lfsEnv) tenantID() string {
	t, _, _ := e.st.GetTenantBySlug(context.Background(), "default")
	return t.ID
}

func TestLFSOidMismatch(t *testing.T) {
	e := newLFSEnv(t)
	data := []byte("payload")
	wrongOID := oidOf([]byte("something else")) // valid hex, wrong content
	if w := e.upload(wrongOID, data); w.Code != 422 {
		t.Fatalf("oid mismatch: want 422 got %d (%s)", w.Code, w.Body.String())
	}
	// Nothing recorded.
	if n, _ := e.st.TenantLFSBytes(context.Background(), e.tenantID()); n != 0 {
		t.Fatalf("mismatch should store nothing, ledger=%d", n)
	}
	// Malformed oid → 422 at both faces.
	if w := e.upload("not-hex", data); w.Code != 422 {
		t.Fatalf("bad oid PUT: want 422 got %d", w.Code)
	}
}

func TestLFSQuota(t *testing.T) {
	e := newLFSEnv(t)
	e.setLFSCap(t, 10) // 10 bytes total

	small := []byte("1234567890") // exactly 10
	soid := oidOf(small)
	if w := e.upload(soid, small); w.Code != 200 {
		t.Fatalf("at-cap upload: want 200 got %d (%s)", w.Code, w.Body.String())
	}

	// Now full: a further object is refused at batch (507 error entry) and at PUT.
	more := []byte("x")
	moid := oidOf(more)
	b := e.batch(t, "upload", []map[string]any{{"oid": moid, "size": len(more)}})
	obj := b["objects"].([]any)[0].(map[string]any)
	if _, hasAction := obj["actions"]; hasAction {
		t.Fatal("over-quota object should not get an upload action")
	}
	em, _ := obj["error"].(map[string]any)
	if em == nil || int(em["code"].(float64)) != http.StatusInsufficientStorage {
		t.Fatalf("want 507 error entry, got %v", obj)
	}
	if w := e.upload(moid, more); w.Code != http.StatusInsufficientStorage {
		t.Fatalf("over-quota PUT: want 507 got %d", w.Code)
	}
}

func TestLFSCrossTenantAndAuth(t *testing.T) {
	e := newLFSEnv(t)
	// No creds → 401.
	r := httptest.NewRequest("POST", "/git/default/shared.git/info/lfs/objects/batch", strings.NewReader("{}"))
	r.SetPathValue("slug", "default")
	r.SetPathValue("repo", "shared.git")
	w := httptest.NewRecorder()
	e.g.lfsBatch(w, r)
	if w.Code != 401 {
		t.Fatalf("no auth: want 401 got %d", w.Code)
	}

	// A member of another tenant cannot use this tenant's LFS endpoint.
	ctx := context.Background()
	sec, _ := e.st.CreateTenant(ctx, "security", "Security")
	id2, _ := e.st.UpsertIdentity(ctx, "s@x", "s-x", "")
	mem2, _ := e.st.EnsureMembership(ctx, id2.ID, sec.ID, "member")
	otherTok := mintGitToken(gitSignKey(e.g.mgr.master32), mem2.ID)

	body, _ := json.Marshal(map[string]any{"operation": "download", "objects": []map[string]any{}})
	r = httptest.NewRequest("POST", "/git/default/shared.git/info/lfs/objects/batch", bytes.NewReader(body))
	r.SetBasicAuth("x-access-token", otherTok)
	r.SetPathValue("slug", "default")
	r.SetPathValue("repo", "shared.git")
	w = httptest.NewRecorder()
	e.g.lfsBatch(w, r)
	if w.Code != 403 {
		t.Fatalf("cross-tenant: want 403 got %d (%s)", w.Code, w.Body.String())
	}
}

// TestLFSRoutePrecedence confirms the LFS routes win over the smart-HTTP catch-all
// on the same mux (registration doesn't panic, and dispatch is correct).
func TestLFSRoutePrecedence(t *testing.T) {
	mux := http.NewServeMux()
	hitLFS, hitGit := false, false
	mux.HandleFunc("POST /git/{slug}/{repo}/info/lfs/objects/batch", func(http.ResponseWriter, *http.Request) { hitLFS = true })
	mux.HandleFunc("/git/{slug}/{repo...}", func(http.ResponseWriter, *http.Request) { hitGit = true })

	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/git/default/shared.git/info/lfs/objects/batch", nil))
	if !hitLFS || hitGit {
		t.Fatalf("LFS batch should route to the LFS handler (lfs=%v git=%v)", hitLFS, hitGit)
	}
	hitLFS, hitGit = false, false
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/git/default/shared.git/git-receive-pack", nil))
	if hitLFS || !hitGit {
		t.Fatalf("git path should route to the git handler (lfs=%v git=%v)", hitLFS, hitGit)
	}
}
