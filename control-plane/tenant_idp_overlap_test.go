package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The approval queue tells the super_admin when a row claims a domain the deployment
// itself signs people in on, and where that domain comes from.
func TestTenantIdPQueueShowsDomainOverlapWithDeployment(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p4Manager(t, st)
	mgr.superAdmins = map[string]bool{"boss@root.example": true}
	tn, err := st.CreateTenant(ctx, "sub", "Sub")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "emails.txt")
	if err := os.WriteFile(file, []byte("# c\nsomeone@x.example\n@filed.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config{
		mgr:             mgr,
		allowDomains:    map[string]bool{"login.example": true},
		allowEmailsFile: file,
		providers:       []auth.LoginProvider{&auth.OIDCProvider{ProviderID: "e", AllowDomains: map[string]bool{"envidp.example": true}}},
	}
	api := newTenantIdPAPI(mgr, cfg.providers)
	api.deployDomains = newDeploymentDomains(cfg)

	seedTenantIdP(t, st, tn.ID, "clash", "login.example,root.example,filed.example,envidp.example,own.example", "pending")

	w := httptest.NewRecorder()
	api.queue(w, httptest.NewRequest(http.MethodGet, "/api/admin/idp", nil), store.Identity{Role: "super_admin"})
	if w.Code != http.StatusOK {
		t.Fatalf("queue: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Providers []tenantIdPBody `json:"providers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Providers) != 1 {
		t.Fatalf("decode: %v %s", err, w.Body.String())
	}
	got := map[string]string{}
	for _, o := range out.Providers[0].DomainOverlaps {
		got[o.Domain] = o.Source
	}
	want := map[string]string{
		"login.example": overlapLoginDomains, "filed.example": overlapLoginDomains,
		"envidp.example": overlapLoginDomains, "root.example": overlapSuperAdmin,
	}
	if len(got) != len(want) {
		t.Fatalf("overlaps = %v, want %v", got, want)
	}
	for d, s := range want {
		if got[d] != s {
			t.Errorf("%s: source %q, want %q", d, got[d], s)
		}
	}
	if _, ok := got["own.example"]; ok {
		t.Error("an unshared domain must not be flagged")
	}
	// The audit line of an edit carries it too.
	if d := overlapAuditDetail(api.overlapsFor("login.example")); d != " domain_overlaps=login.example:login_domains" {
		t.Errorf("audit detail = %q", d)
	}
	// Without a deployment view nothing is computed.
	if o := newTenantIdPAPI(mgr, nil).overlapsFor("login.example"); o != nil {
		t.Errorf("no deployment view: %v", o)
	}
}
