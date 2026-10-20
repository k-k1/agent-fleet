package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func callExternalMemory(mgr *manager, email, method, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/admin/tenants/sales/external-memory", strings.NewReader(body))
	r.SetPathValue("slug", "sales")
	r.Header.Set("X-Forwarded-Email", email)
	w := httptest.NewRecorder()
	if method == http.MethodGet {
		newAdminAPI(mgr).tenantExternalMemory(w, r)
	} else {
		newAdminAPI(mgr).setTenantExternalMemory(w, r)
	}
	return w
}

// A tenant_admin reads the switch (on by default), turns it off and on, and each change is audited.
func TestTenantExternalMemorySwitch(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, _, _ := networkFixture(t)

	w := callExternalMemory(mgr, "boss@acme.co.jp", http.MethodGet, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatalf("default = %d %s, want enabled", w.Code, w.Body.String())
	}
	w = callExternalMemory(mgr, "boss@acme.co.jp", http.MethodPut, `{"enabled":false}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("off = %d %s", w.Code, w.Body.String())
	}
	if on, err := st.GetTenantExternalMemory(ctx, tn.ID); err != nil || on {
		t.Fatalf("stored = %v, %v; want off", on, err)
	}
	if w = callExternalMemory(mgr, "boss@acme.co.jp", http.MethodPut, `{"enabled":true}`); w.Code != http.StatusOK {
		t.Fatalf("on = %d %s", w.Code, w.Body.String())
	}
	rows, err := st.ListAuditByTenant(ctx, tn.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	var details []string
	for _, r := range rows {
		if r.Action == "tenant.external_memory" && r.Target == "sales" {
			details = append(details, r.Detail)
		}
	}
	if len(details) != 2 {
		t.Fatalf("audit rows = %v, want two tenant.external_memory entries", details)
	}
}

// A plain member can neither read nor change it, and a body without `enabled` is refused rather
// than read as off.
func TestTenantExternalMemorySwitchGuards(t *testing.T) {
	st, mgr, tn, _, _ := networkFixture(t)
	for _, m := range []string{http.MethodGet, http.MethodPut} {
		if w := callExternalMemory(mgr, "yamada@acme.co.jp", m, `{"enabled":false}`); w.Code != http.StatusForbidden {
			t.Errorf("member %s = %d %s, want 403", m, w.Code, w.Body.String())
		}
	}
	if w := callExternalMemory(mgr, "boss@acme.co.jp", http.MethodPut, `{}`); w.Code != http.StatusBadRequest {
		t.Errorf("empty body = %d, want 400", w.Code)
	}
	if on, _ := st.GetTenantExternalMemory(context.Background(), tn.ID); !on {
		t.Error("a refused write must leave the switch on")
	}
}
