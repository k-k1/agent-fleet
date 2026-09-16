package main

// tenant_limits_engine_gate_test.go — ADR 0084 decision 7: the write and read paths around
// tenantLimits.AllowEngineLLM / AllowEngineImage (PUT /api/admin/tenants/{slug}/limits and
// GET /api/admin/tenants), plus decisions 7 and 9's side effects (audit, the tenant-scoped
// catalogue push). The gateway's own read of these fields (catalog / token / serve) is
// engine_tenant_gate_test.go; this file is the admin surface around them.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// A save that never mentions allow_engine_llm/allow_engine_image (an older Console build, or
// a field this test does not set) must leave the tenant's limits blob with nil for both — the
// same "nobody has said anything" state a brand-new tenant starts in — never an implicit
// false. tenantLimitsProjectionCoversEveryStoredField and the round-trip test already pin the
// struct-level contract; this pins the HTTP handler actually preserves it end to end.
func TestSetTenantLimitsOmittedEngineGateStaysNil(t *testing.T) {
	ctx := context.Background()
	st, mgr := budgetFixture(t, 30, map[string]int{"acme": 4})
	w := putLimits(mgr, "acme", `{"max_workspaces":4}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", w.Code, w.Body.String())
	}
	tn, _, _ := st.GetTenantBySlug(ctx, "acme")
	lim := parseLimits(tn.Limits)
	if lim.AllowEngineLLM != nil || lim.AllowEngineImage != nil {
		t.Fatalf("allow_engine_llm/image = %v/%v, want both nil (unset, not denied)",
			lim.AllowEngineLLM, lim.AllowEngineImage)
	}
}

// A super_admin's explicit false rewrites the blob and stays false — the mirror image of the
// test above, so a nil bug and an "always true" bug cannot both hide behind the same test.
func TestSetTenantLimitsWritesAnExplicitEngineDenial(t *testing.T) {
	ctx := context.Background()
	st, mgr := budgetFixture(t, 30, map[string]int{"acme": 4})
	w := putLimits(mgr, "acme", `{"max_workspaces":4,"allow_engine_llm":false,"allow_engine_image":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", w.Code, w.Body.String())
	}
	tn, _, _ := st.GetTenantBySlug(ctx, "acme")
	lim := parseLimits(tn.Limits)
	if lim.AllowEngineLLM == nil || *lim.AllowEngineLLM {
		t.Errorf("allow_engine_llm = %v, want an explicit false", lim.AllowEngineLLM)
	}
	if lim.AllowEngineImage == nil || !*lim.AllowEngineImage {
		t.Errorf("allow_engine_image = %v, want an explicit true", lim.AllowEngineImage)
	}
	// And the response echoes what was actually written, not a resolved default — the Console
	// re-syncs its checkboxes from this on every save.
	var resp struct {
		AllowEngineLLM   *bool `json:"allow_engine_llm"`
		AllowEngineImage *bool `json:"allow_engine_image"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.AllowEngineLLM == nil || *resp.AllowEngineLLM {
		t.Errorf("response allow_engine_llm = %v, want false", resp.AllowEngineLLM)
	}
	if resp.AllowEngineImage == nil || !*resp.AllowEngineImage {
		t.Errorf("response allow_engine_image = %v, want true", resp.AllowEngineImage)
	}
}

// SetTenantLimits had no audit trail at all before ADR 0084 (⚠️ in the ADR). A switch that
// can take image generation and self-hosted chat away from a whole tenant needs one.
func TestSetTenantLimitsWritesAnAuditRow(t *testing.T) {
	ctx := context.Background()
	st, mgr := budgetFixture(t, 30, map[string]int{"acme": 4})
	tn, _, _ := st.GetTenantBySlug(ctx, "acme")

	if w := putLimits(mgr, "acme", `{"max_workspaces":4,"allow_engine_llm":false}`); w.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", w.Code, w.Body.String())
	}

	rows, err := st.ListAuditByTenant(ctx, tn.ID, 0)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.Action == "tenant.limits" && r.ActorID == "I-boss" {
			found = true
		}
	}
	if !found {
		t.Errorf("no tenant.limits audit row for I-boss among %+v", rows)
	}
}

// ADR 0084 decision 9: a save must push the change to the tenant's running workspaces at
// once, not leave it to the Agent's own TTL — that up-to-ten-minute window is exactly what
// decision 8's gates 2/3 exist to cover, and decision 9 says close it from the push side too.
func TestSetTenantLimitsPushesTheTenantScopedCatalogChange(t *testing.T) {
	pushed := make(chan string, 4)
	orig := notifyEngineCatalogChangedForTenant
	notifyEngineCatalogChangedForTenant = func(_ context.Context, _ *manager, tenantID, _ string) {
		pushed <- tenantID
	}
	t.Cleanup(func() { notifyEngineCatalogChangedForTenant = orig })

	st, mgr := budgetFixture(t, 30, map[string]int{"acme": 4})
	tn, _, _ := st.GetTenantBySlug(context.Background(), "acme")

	if w := putLimits(mgr, "acme", `{"max_workspaces":4,"allow_engine_llm":false}`); w.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", w.Code, w.Body.String())
	}
	// Wait for it rather than reading the channel non-blockingly: PushEngineCatalogChanged
	// (tenant_wiring.go) dispatches on its own goroutine, so a `default:` branch here asserts
	// that the Go scheduler happened to run that goroutine before this line — true on an idle
	// machine, false on a loaded CI runner. Reproduced deterministically with GOMAXPROCS=1,
	// where the spawned goroutine cannot run until this one blocks.
	select {
	case got := <-pushed:
		if got != tn.ID {
			t.Errorf("pushed tenant = %q, want %q", got, tn.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SetTenantLimits did not push a tenant-scoped catalogue change")
	}
}

// The gate is super_admin's business (ADR 0084 decision 7): "may this tenant spend on a GPU
// box at all" is not something a tenant_admin decides about their own tenant, so their own
// admin roster row must not even carry the field — same rule engine_ingest_perm.go already
// applies to the operator-only columns on the engine panel.
func TestListTenantsOmitsEngineGateFromATenantAdminsOwnRow(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	acme, err := st.CreateTenant(ctx, "acme", "Acme")
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}
	if err := st.SetTenantLimits(ctx, acme.ID, `{"allow_engine_llm":false}`); err != nil {
		t.Fatalf("limits: %v", err)
	}
	ident, err := st.UpsertIdentity(ctx, "akira@example.com", "akira", "")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	if _, err := st.EnsureMembership(ctx, ident.ID, acme.ID, "tenant_admin"); err != nil {
		t.Fatalf("membership: %v", err)
	}

	// Positive control: the super_admin's OWN call for the same tenant does carry it — a
	// scan that always omits the field would pass the tenant_admin case below for the wrong
	// reason.
	r := httptest.NewRequest(http.MethodGet, "/api/admin/tenants", nil)
	w := httptest.NewRecorder()
	newAdminAPI(mgr).listTenants(w, r, store.Identity{ID: "I-boss", Role: "super_admin"})
	if w.Code != http.StatusOK {
		t.Fatalf("super_admin listTenants = %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "allow_engine_llm") {
		t.Fatalf("positive control: super_admin's own listing has no allow_engine_llm: %s", w.Body.String())
	}

	r = httptest.NewRequest(http.MethodGet, "/api/admin/tenants", nil)
	w = httptest.NewRecorder()
	newAdminAPI(mgr).listTenants(w, r, ident)
	if w.Code != http.StatusOK {
		t.Fatalf("tenant_admin listTenants = %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "allow_engine_llm") || strings.Contains(w.Body.String(), "allow_engine_image") {
		t.Errorf("a tenant_admin's own row carries the engine gate: %s", w.Body.String())
	}
}
