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
		"login.example": auth.OverlapLoginDomains, "filed.example": auth.OverlapLoginDomains,
		"envidp.example": auth.OverlapLoginDomains, "root.example": auth.OverlapSuperAdmin,
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
	// Without a deployment view nothing is computed.
	if o := newTenantIdPAPI(mgr, nil).overlapsFor("login.example"); o != nil {
		t.Errorf("no deployment view: %v", o)
	}
}

// The overlap reaches the audit trail as a deployment-wide row: a super_admin's view has it, and
// a tenant_admin's tenant-scoped read (what GET /api/admin/audit and the MCP audit tail return
// for them) has no trace of it, in any row's detail.
func TestTenantIdPOverlapAuditIsDeploymentWideOnly(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p4Manager(t, st)
	tn, err := st.CreateTenant(ctx, "sub", "Sub")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config{mgr: mgr, allowDomains: map[string]bool{"login.example": true}}
	api := newTenantIdPAPI(mgr, nil)
	api.deployDomains = newDeploymentDomains(cfg)
	row := seedTenantIdP(t, st, tn.ID, "clash", "login.example,own.example", "pending")

	r := httptest.NewRequest(http.MethodPost, "/", nil)
	ident := store.Identity{ID: "boss", Role: "super_admin"}
	api.audit(r, ident, tn.ID, "tenant_idp.active", "t:sub:clash", "issuer="+row.Issuer)
	api.auditOverlap(r, ident, "sub", "tenant_idp.active", "t:sub:clash", row.AllowedDomains)

	own, err := st.ListAuditByTenant(ctx, tn.ID, 50)
	if err != nil || len(own) != 1 {
		t.Fatalf("tenant-scoped audit: %v %+v", err, own)
	}
	for _, l := range own {
		if strings.Contains(l.Detail, "login.example") || strings.Contains(l.Detail, "overlap") || strings.Contains(l.Action, "overlap") {
			t.Fatalf("a tenant-scoped audit row carries the overlap: %+v", l)
		}
	}
	all, _ := st.ListAuditByTenant(ctx, "", 50)
	found := false
	for _, l := range all {
		if l.Action == "tenant_idp.domain_overlap" && l.TenantID == "" &&
			strings.Contains(l.Detail, "login.example:login_domains") && !strings.Contains(l.Detail, "own.example") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the super_admin view has no overlap row: %+v", all)
	}
}
