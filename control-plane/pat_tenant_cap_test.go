package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Issue #3465: a tenant may cap PAT lifetime below the deployment cap. The lower of the two
// applies at issuance and on every use of an existing token.

func TestPATIssuanceUsesTheLowerOfDeploymentAndTenantCap(t *testing.T) {
	t.Parallel()
	f := newAccessFixture(t)
	f.mgr.patMaxDays = 30
	if err := f.st.SetTenantLimits(context.Background(), f.beta.ID, `{"pat_max_days":10}`); err != nil {
		t.Fatal(err)
	}
	api := newPATAPI(f.mgr)
	mint := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		api.create(w, f.req("POST", "/api/pat", body, "boss@example.com", "beta", "entra", inOffice), f.boss)
		return w
	}
	if w := mint(`{"name":"t","ttl_days":11}`); w.Code != 400 || accessErrCode(w) != "invalid_ttl" {
		t.Errorf("above the tenant cap: %d %s", w.Code, w.Body)
	}
	if w := mint(`{"name":"t","ttl_days":10}`); w.Code != 200 {
		t.Errorf("at the tenant cap: %d %s", w.Code, w.Body)
	}
	// Default lifetime (90d) is clamped to the tenant cap, not the deployment's.
	w := mint(`{"name":"t"}`)
	if w.Code != 200 {
		t.Fatalf("default: %d %s", w.Code, w.Body)
	}
	exp, err := time.Parse(time.RFC3339, accessJSONField(w.Body.String(), "expires_at"))
	if err != nil || exp.After(time.Now().AddDate(0, 0, 10).Add(time.Minute)) {
		t.Errorf("default lifetime not clamped to 10 days: %v %v", exp, err)
	}
	// A tenant cap above the deployment cap does not loosen it.
	if err := f.st.SetTenantLimits(context.Background(), f.beta.ID, `{"pat_max_days":300}`); err != nil {
		t.Fatal(err)
	}
	if w := mint(`{"name":"t","ttl_days":31}`); w.Code != 400 {
		t.Errorf("tenant cap must not exceed the deployment cap: %d %s", w.Code, w.Body)
	}
	// A tenant with no cap gets the deployment cap.
	nw := httptest.NewRecorder()
	api.create(nw, f.req("POST", "/api/pat", `{"name":"t","ttl_days":30}`, "boss@example.com", "acme", "entra", inOffice), f.boss)
	if nw.Code != 200 {
		t.Errorf("tenant without a cap: %d %s", nw.Code, nw.Body)
	}
}

func TestPATUseHonorsTheTenantCapOfTheTokensOwnTenant(t *testing.T) {
	t.Parallel()
	e := newMCPOAuthEnv(t)
	ctx := context.Background()
	other, err := e.mgr.store.CreateTenant(ctx, "other", "Other")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(id string, age, ttl int) string {
		tok, h := newPATToken()
		created := time.Now().AddDate(0, 0, -age).UTC().Format(time.RFC3339)
		expires := ""
		if ttl > 0 {
			expires = time.Now().AddDate(0, 0, ttl).UTC().Format(time.RFC3339)
		}
		if err := e.mgr.store.CreatePAT(ctx, store.PAT{ID: id, IdentityID: e.ident.ID, MembershipID: e.membershipID(),
			Scope: "read", Name: id, CreatedAt: created, ExpiresAt: expires}, h); err != nil {
			t.Fatal(err)
		}
		return tok
	}
	call := func(tok string) int { return e.mcpCall(tok, "tools/list").Code }
	old := mk("old", 20, 60) // issued under a looser cap, still inside its own expiry
	fresh := mk("fresh", 2, 60)
	legacy := mk("legacy", 20, 0) // no expiry at all

	// Another tenant's cap must not touch this token, whatever the caller sends.
	if err := e.mgr.store.SetTenantLimits(ctx, other.ID, `{"pat_max_days":1}`); err != nil {
		t.Fatal(err)
	}
	if c := call(old); c != 200 {
		t.Fatalf("no cap on the token's tenant: %d, want 200", c)
	}

	if err := e.mgr.store.SetTenantLimits(ctx, e.tenA.ID, `{"pat_max_days":10}`); err != nil {
		t.Fatal(err)
	}
	if c := call(old); c != 401 {
		t.Errorf("token older than the lowered tenant cap: %d, want 401", c)
	}
	if c := call(legacy); c != 401 {
		t.Errorf("non-expiring token older than the tenant cap: %d, want 401", c)
	}
	if c := call(fresh); c != 200 {
		t.Errorf("token inside the tenant cap: %d, want 200", c)
	}

	// An unreadable blob and a nonsense value fall back to the deployment cap, never to "no cap".
	e.mgr.patMaxDays = 5
	for _, blob := range []string{`{not json`, `{"pat_max_days":-3}`, `{"pat_max_days":99999}`, ``} {
		if err := e.mgr.store.SetTenantLimits(ctx, e.tenA.ID, blob); err != nil {
			t.Fatal(err)
		}
		if c := call(old); c != 401 {
			t.Errorf("limits %q: token past the deployment cap authenticated: %d", blob, c)
		}
		if c := call(fresh); c != 200 {
			t.Errorf("limits %q: token inside the deployment cap: %d, want 200", blob, c)
		}
	}
}

func TestSetTenantLimitsValidatesAndKeepsPATMaxDays(t *testing.T) {
	t.Parallel()
	_, mgr := budgetFixture(t, 30, map[string]int{"acme": 4})
	for _, bad := range []string{`{"pat_max_days":-1}`, `{"pat_max_days":3651}`} {
		if w := putLimits(mgr, "acme", bad); w.Code != 400 || !strings.Contains(w.Body.String(), "pat_max_days") {
			t.Errorf("%s = %d %s, want 400 naming the field", bad, w.Code, w.Body)
		}
	}
	if w := putLimits(mgr, "acme", `{"max_workspaces":4,"pat_max_days":90}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"pat_max_days":90`) {
		t.Fatalf("valid value: %d %s", w.Code, w.Body)
	}
	tn, _, _ := mgr.store.GetTenantBySlug(context.Background(), "acme")
	if got := parseLimits(tn.Limits).PATMaxDays; got != 90 {
		t.Errorf("stored pat_max_days = %d, want 90", got)
	}
	if w := putLimits(mgr, "acme", `{"max_workspaces":4}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	tn, _, _ = mgr.store.GetTenantBySlug(context.Background(), "acme")
	if got := parseLimits(tn.Limits).PATMaxDays; got != 0 {
		t.Errorf("0/omitted must clear the tenant cap, got %d", got)
	}
}

// failTenantLookup fails the PAT-to-tenant lookup the on-use cap depends on.
type failTenantLookup struct{ store.Store }

func (failTenantLookup) PATTenantID(context.Context, string) (string, error) {
	return "", errors.New("transient")
}

// When the cap cannot be resolved the token must be refused, not honored uncapped.
func TestPATIsRefusedWhenItsCapCannotBeResolved(t *testing.T) {
	t.Parallel()
	e := newMCPOAuthEnv(t)
	tok, h := newPATToken()
	if err := e.mgr.store.CreatePAT(context.Background(), store.PAT{ID: "p", IdentityID: e.ident.ID, MembershipID: e.membershipID(),
		Scope: "read", Name: "p", CreatedAt: store.NowTS(), ExpiresAt: time.Now().AddDate(0, 0, 5).UTC().Format(time.RFC3339)}, h); err != nil {
		t.Fatal(err)
	}
	if c := e.mcpCall(tok, "tools/list").Code; c != 200 {
		t.Fatalf("positive control: %d, want 200", c)
	}
	e.mgr.store = failTenantLookup{e.mgr.store}
	if c := e.mcpCall(tok, "tools/list").Code; c != 500 {
		t.Errorf("unresolvable cap: %d, want 500 (fail closed)", c)
	}
}
