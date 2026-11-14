package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func callMemoQueuesSwitch(mgr *manager, email, method, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/admin/tenants/sales/memo-queues", strings.NewReader(body))
	r.SetPathValue("slug", "sales")
	r.Header.Set("X-Forwarded-Email", email)
	w := httptest.NewRecorder()
	if method == http.MethodGet {
		newAdminAPI(mgr).tenantMemoQueues(w, r)
	} else {
		newAdminAPI(mgr).setTenantMemoQueues(w, r)
	}
	return w
}

// The memo-queue switch is off by default, a tenant_admin turns it on and off, and each change
// is audited.
func TestTenantMemoQueuesSwitch(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, _, _ := networkFixture(t)

	w := callMemoQueuesSwitch(mgr, "boss@acme.co.jp", http.MethodGet, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("default = %d %s, want off", w.Code, w.Body.String())
	}
	if w = callMemoQueuesSwitch(mgr, "boss@acme.co.jp", http.MethodPut, `{"enabled":true}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatalf("on = %d %s", w.Code, w.Body.String())
	}
	if on, err := st.GetTenantMemoQueues(ctx, tn.ID); err != nil || !on {
		t.Fatalf("stored = %v, %v; want on", on, err)
	}
	if w = callMemoQueuesSwitch(mgr, "boss@acme.co.jp", http.MethodPut, `{"enabled":false}`); w.Code != http.StatusOK {
		t.Fatalf("off = %d %s", w.Code, w.Body.String())
	}
	rows, err := st.ListAuditByTenant(ctx, tn.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rows {
		if r.Action == "tenant.memo_queues" && r.Target == "sales" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("audit rows = %d, want two tenant.memo_queues entries", n)
	}
}

// A plain member can neither read nor change it, and a body without `enabled` is refused.
func TestTenantMemoQueuesSwitchGuards(t *testing.T) {
	st, mgr, tn, _, _ := networkFixture(t)
	for _, m := range []string{http.MethodGet, http.MethodPut} {
		if w := callMemoQueuesSwitch(mgr, "yamada@acme.co.jp", m, `{"enabled":true}`); w.Code != http.StatusForbidden {
			t.Errorf("member %s = %d %s, want 403", m, w.Code, w.Body.String())
		}
	}
	if w := callMemoQueuesSwitch(mgr, "boss@acme.co.jp", http.MethodPut, `{}`); w.Code != http.StatusBadRequest {
		t.Errorf("empty body = %d, want 400", w.Code)
	}
	if on, _ := st.GetTenantMemoQueues(context.Background(), tn.ID); on {
		t.Error("a refused write must leave the switch off")
	}
}
