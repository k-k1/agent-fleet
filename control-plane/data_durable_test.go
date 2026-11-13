package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/datalayout"
)

func TestInternalGitCreateRefusedWhenDataNotDurable(t *testing.T) {
	ctx := context.Background()
	st, err := openMigratedSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	dflt, _ := st.EnsureDefaultTenant(ctx)
	ident, _ := st.UpsertIdentity(ctx, "u@x", "u-x", "")
	if _, err := st.EnsureMembership(ctx, ident.ID, dflt.ID, "member"); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	mgr := &manager{store: st, authMode: "proxy", emailHeader: "X-Forwarded-Email", dataRoot: root}
	g := newGitServerAPI(mgr, "https://fleet.example.com")
	create := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/internal-git/repos", strings.NewReader(`{"name":"proj"}`))
		r.Header.Set("X-Forwarded-Email", "u@x")
		w := httptest.NewRecorder()
		g.withMembership(g.repoCreate)(w, r)
		return w
	}

	mgr.gitDataRefusal = gitDataNotDurableMsg
	w := create()
	var body struct {
		Error struct{ Code, Message string }
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusServiceUnavailable || body.Error.Code != errCodeGitDataNotDurable || !strings.Contains(body.Error.Message, envDataDurableRequired) {
		t.Fatalf("refused create: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(datalayout.GitRepoDir(root, "default", "proj")); err == nil {
		t.Error("a bare repository was created despite the refusal")
	}
	if repos, _ := st.ListGitReposByTenant(ctx, dflt.ID); len(repos) != 0 {
		t.Errorf("a ledger row was created despite the refusal: %v", repos)
	}

	mgr.gitDataRefusal = ""
	if w := create(); w.Code != http.StatusOK {
		t.Fatalf("create with the guard off: %d %s", w.Code, w.Body.String())
	}
}

func TestInternalGitHTTPRefusedWhenDataNotDurable(t *testing.T) {
	e := newLFSEnv(t)
	oid := oidOf([]byte("x"))
	routes := []struct{ method, path string }{
		{"GET", "/git/default/shared.git/info/refs?service=git-upload-pack"},
		{"POST", "/git/default/shared.git/git-receive-pack"},
		{"POST", "/git/default/shared.git/info/lfs/objects/batch"},
		{"PUT", "/git/default/shared.git/info/lfs/objects/" + oid},
		{"POST", "/git/default/shared.git/info/lfs/locks"},
	}
	serve := func(method, path string) *httptest.ResponseRecorder {
		mux := http.NewServeMux()
		registerInternalGitRoutes(mux, config{mgr: e.g.mgr, publicBaseURL: "https://fleet.example.com"})
		r := httptest.NewRequest(method, path, strings.NewReader("{}"))
		r.SetBasicAuth("x-access-token", e.token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	e.g.mgr.gitDataRefusal = gitDataNotDurableMsg
	for _, rt := range routes {
		w := serve(rt.method, rt.path)
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), envDataDurableRequired) {
			t.Errorf("%s %s: got %d (%s), want 503 naming %s", rt.method, rt.path, w.Code, w.Body.String(), envDataDurableRequired)
		}
		if strings.Contains(rt.path, "/info/lfs/") && !strings.HasPrefix(w.Header().Get("Content-Type"), lfsContentType) {
			t.Errorf("%s %s: LFS refusal is not an LFS error body", rt.method, rt.path)
		}
	}
	e.g.mgr.gitDataRefusal = ""
	for _, rt := range routes {
		if w := serve(rt.method, rt.path); w.Code == http.StatusServiceUnavailable {
			t.Errorf("%s %s: 503 with the guard off (%s)", rt.method, rt.path, w.Body.String())
		}
	}
}
