package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// #1080: a deployment that signs real people in without a master key stores their credentials
// in plaintext, and says so. dev is the one mode where that is the intent.
func TestPlaintextSecretsWarning(t *testing.T) {
	key := make([]byte, 32)
	for _, c := range []struct {
		auth   string
		keyed  bool
		expect bool
	}{
		{"dev", false, false},
		{"dev", true, false},
		{"oauth", false, true},
		{"proxy", false, true},
		{"oauth", true, false},
	} {
		m := &manager{authMode: c.auth}
		if c.keyed {
			m.master32 = key
			m.custodian = newLocalCustodian(key)
		}
		if got := m.plaintextSecrets(); got != c.expect {
			t.Errorf("AUTH=%s keyed=%v: plaintextSecrets=%v, want %v", c.auth, c.keyed, got, c.expect)
		}
		warned := len(m.deploymentWarnings()) == 1 && m.deploymentWarnings()[0] == deploymentWarnPlaintextSecrets
		if warned != c.expect {
			t.Errorf("AUTH=%s keyed=%v: deploymentWarnings=%v", c.auth, c.keyed, m.deploymentWarnings())
		}
	}
}

// The super_admin's tenant list carries the warning (the Console's admin modal shows it as a
// banner); a tenant_admin's does not, since only the operator can set the key.
func TestAdminTenantsCarriesDeploymentWarnings(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st) // AUTH=oauth, no master key
	tn, err := st.CreateTenant(ctx, "sales", "営業部")
	if err != nil {
		t.Fatal(err)
	}
	ta, _ := st.UpsertIdentity(ctx, "ta@acme.co.jp", "ta-acme-co-jp", "")
	if _, err := st.EnsureMembership(ctx, ta.ID, tn.ID, "tenant_admin"); err != nil {
		t.Fatal(err)
	}
	list := func(ident store.Identity) map[string]json.RawMessage {
		t.Helper()
		w := httptest.NewRecorder()
		newAdminAPI(mgr).listTenants(w, httptest.NewRequest(http.MethodGet, "/api/admin/tenants", nil), ident)
		var out map[string]json.RawMessage
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("list = %d %s", w.Code, w.Body.String())
		}
		return out
	}

	if got := string(list(store.Identity{ID: "I-1", Role: "super_admin"})["deployment_warnings"]); got != `["plaintext_secrets"]` {
		t.Errorf("super_admin without a master key: deployment_warnings = %s, want [\"plaintext_secrets\"]", got)
	}
	if got, ok := list(ta)["deployment_warnings"]; ok {
		t.Errorf("a tenant_admin was shown deployment warnings: %s", got)
	}

	mgr.master32 = make([]byte, 32)
	mgr.custodian = newLocalCustodian(mgr.master32)
	if got := string(list(store.Identity{ID: "I-1", Role: "super_admin"})["deployment_warnings"]); got != `[]` {
		t.Errorf("super_admin with a master key: deployment_warnings = %s, want []", got)
	}
}
