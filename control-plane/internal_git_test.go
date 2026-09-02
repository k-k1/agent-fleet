package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// TestInternalGitListTenantScoped verifies the repo-management list is confined to
// the caller's resolved tenant: a member of two tenants sees only the repos of the
// tenant selected via X-AF-Tenant, never the other's.
func TestInternalGitListTenantScoped(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	dflt, _ := st.EnsureDefaultTenant(ctx)
	sec, err := st.CreateTenant(ctx, "security", "Security")
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}
	ident, err := st.UpsertIdentity(ctx, "u@x", "u-x", "")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	if _, err := st.EnsureMembership(ctx, ident.ID, dflt.ID, "member"); err != nil {
		t.Fatalf("mem default: %v", err)
	}
	if _, err := st.EnsureMembership(ctx, ident.ID, sec.ID, "member"); err != nil {
		t.Fatalf("mem security: %v", err)
	}
	must := func(g store.GitRepo) {
		if err := st.CreateGitRepo(ctx, g); err != nil {
			t.Fatalf("repo: %v", err)
		}
	}
	must(store.GitRepo{ID: store.NewID(), TenantID: dflt.ID, Name: "alpha", DefaultBranch: "main", CreatedAt: store.NowTS()})
	must(store.GitRepo{ID: store.NewID(), TenantID: sec.ID, Name: "bravo", DefaultBranch: "main", CreatedAt: store.NowTS()})

	g := newGitServerAPI(&manager{store: st, authMode: "proxy", emailHeader: "X-Forwarded-Email"},
		"https://fleet.example.com")

	list := func(tenant string) []string {
		r := httptest.NewRequest("GET", "/api/internal-git/repos", nil)
		r.Header.Set("X-Forwarded-Email", "u@x")
		r.Header.Set("X-AF-Tenant", tenant)
		w := httptest.NewRecorder()
		g.withMembership(g.reposList)(w, r)
		if w.Code != 200 {
			t.Fatalf("tenant %s: status %d (%s)", tenant, w.Code, w.Body.String())
		}
		var body struct {
			Repos []struct {
				Name     string `json:"name"`
				CloneURL string `json:"clone_url"`
			} `json:"repos"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		names := make([]string, 0, len(body.Repos))
		for _, r := range body.Repos {
			names = append(names, r.Name)
		}
		return names
	}

	if got := list("default"); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("default tenant list = %v, want [alpha]", got)
	}
	if got := list("security"); len(got) != 1 || got[0] != "bravo" {
		t.Fatalf("security tenant list = %v, want [bravo]", got)
	}

	// clone_url uses the public base host.
	r := httptest.NewRequest("GET", "/api/internal-git/repos", nil)
	r.Header.Set("X-Forwarded-Email", "u@x")
	r.Header.Set("X-AF-Tenant", "default")
	w := httptest.NewRecorder()
	g.withMembership(g.reposList)(w, r)
	if want := "https://fleet.example.com/git/default/alpha.git"; !strings.Contains(w.Body.String(), want) {
		t.Fatalf("clone_url missing %q in %s", want, w.Body.String())
	}
}
