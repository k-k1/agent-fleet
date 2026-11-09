package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func callWatchSources(mgr *manager, email, method, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/admin/tenants/sales/watch-sources", strings.NewReader(body))
	r.SetPathValue("slug", "sales")
	r.Header.Set("X-Forwarded-Email", email)
	w := httptest.NewRecorder()
	if method == http.MethodGet {
		newAdminAPI(mgr).tenantWatchSources(w, r)
	} else {
		newAdminAPI(mgr).setTenantWatchSources(w, r)
	}
	return w
}

// The switch is off by default, a tenant_admin turns it on and off, and each change is audited.
func TestTenantWatchSourcesSwitch(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, _, _ := networkFixture(t)

	w := callWatchSources(mgr, "boss@acme.co.jp", http.MethodGet, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("default = %d %s, want off", w.Code, w.Body.String())
	}
	if w = callWatchSources(mgr, "boss@acme.co.jp", http.MethodPut, `{"enabled":true}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatalf("on = %d %s", w.Code, w.Body.String())
	}
	if on, err := st.GetTenantWatchSources(ctx, tn.ID); err != nil || !on {
		t.Fatalf("stored = %v, %v; want on", on, err)
	}
	if w = callWatchSources(mgr, "boss@acme.co.jp", http.MethodPut, `{"enabled":false}`); w.Code != http.StatusOK {
		t.Fatalf("off = %d %s", w.Code, w.Body.String())
	}
	rows, err := st.ListAuditByTenant(ctx, tn.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rows {
		if r.Action == "tenant.watch_sources" && r.Target == "sales" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("audit rows = %d, want two tenant.watch_sources entries", n)
	}
}

// A plain member can neither read nor change it, and a body without `enabled` is refused.
func TestTenantWatchSourcesSwitchGuards(t *testing.T) {
	st, mgr, tn, _, _ := networkFixture(t)
	for _, m := range []string{http.MethodGet, http.MethodPut} {
		if w := callWatchSources(mgr, "yamada@acme.co.jp", m, `{"enabled":true}`); w.Code != http.StatusForbidden {
			t.Errorf("member %s = %d %s, want 403", m, w.Code, w.Body.String())
		}
	}
	if w := callWatchSources(mgr, "boss@acme.co.jp", http.MethodPut, `{}`); w.Code != http.StatusBadRequest {
		t.Errorf("empty body = %d, want 400", w.Code)
	}
	if on, _ := st.GetTenantWatchSources(context.Background(), tn.ID); on {
		t.Error("a refused write must leave the switch off")
	}
}

// A registration that marks a server as a watch source is refused while the tenant switch is off
// and reaches the Agent, body intact, while it is on. Anything else passes either way.
const plainBody = `{"name":"x","targets":{"session":true}}`

func TestGateMCPWatch(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, mv, _ := networkFixture(t)
	cfg := config{mgr: mgr}
	var got string
	next := func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.WriteHeader(http.StatusNoContent)
	}
	call := func(body string) *httptest.ResponseRecorder {
		got = ""
		r := httptest.NewRequest(http.MethodPost, "/api/mcp-servers", bytes.NewReader([]byte(body)))
		w := httptest.NewRecorder()
		cfg.gateMCPWatch(next)(w, r, &resolved{mv: store.MembershipView{MembershipID: mv.MembershipID, TenantID: tn.ID}})
		return w
	}
	const watch = `{"name":"x","targets":{"session":true,"watch":true}}`
	// The Agent reads the first JSON value only, so a body with something after it is still a source.
	for _, b := range []string{watch + " trailing-garbage", watch + "\n" + plainBody} {
		if w := call(b); w.Code != http.StatusForbidden || got != "" {
			t.Errorf("%q passed the gate with the switch off: %d (forwarded %q)", b, w.Code, got)
		}
	}
	// A value that does not end within the limit cannot be judged, and is refused.
	if w := call(`{"name":"` + strings.Repeat("a", maxMCPBody) + `"}`); w.Code != http.StatusRequestEntityTooLarge || got != "" {
		t.Errorf("oversize: %d (forwarded %d bytes)", w.Code, len(got))
	}
	const plain = `{"name":"x","targets":{"session":true}}`

	if w := call(watch); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), codeMCPWatchDisabled) || got != "" {
		t.Fatalf("switch off: %d %s (forwarded %q)", w.Code, w.Body.String(), got)
	}
	for _, b := range []string{plain, `{"name":"x","targets":{"watch":false}}`, `not json`} {
		if w := call(b); w.Code != http.StatusNoContent || got != b {
			t.Errorf("%q: %d, forwarded %q", b, w.Code, got)
		}
	}
	if err := st.SetTenantWatchSources(ctx, tn.ID, true); err != nil {
		t.Fatal(err)
	}
	if w := call(watch); w.Code != http.StatusNoContent || got != watch {
		t.Fatalf("switch on: %d, forwarded %q", w.Code, got)
	}
	// An unknown tenant is off, not on.
	r := httptest.NewRequest(http.MethodPost, "/api/mcp-servers", strings.NewReader(watch))
	w := httptest.NewRecorder()
	cfg.gateMCPWatch(next)(w, r, &resolved{mv: store.MembershipView{TenantID: "nope"}})
	if w.Code != http.StatusForbidden {
		t.Errorf("unreadable switch: %d", w.Code)
	}
}

// A tenant_admin of another tenant can neither read nor change this tenant's switch, and nothing
// is audited for the refused write.
func TestTenantWatchSourcesOtherTenantAdminRefused(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, _, _ := networkFixture(t)
	other, _ := st.CreateTenant(ctx, "ops", "Ops")
	rival, _ := st.UpsertIdentity(ctx, "rival@other.co.jp", "rival-other-co-jp", "tenant_admin")
	if _, err := st.EnsureMembership(ctx, rival.ID, other.ID, "tenant_admin"); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{http.MethodGet, http.MethodPut} {
		if w := callWatchSources(mgr, "rival@other.co.jp", m, `{"enabled":true}`); w.Code != http.StatusForbidden {
			t.Errorf("other tenant's admin %s = %d %s, want 403", m, w.Code, w.Body.String())
		}
	}
	if on, _ := st.GetTenantWatchSources(ctx, tn.ID); on {
		t.Error("another tenant's admin turned the switch on")
	}
	rows, _ := st.ListAuditByTenant(ctx, tn.ID, 10)
	for _, r := range rows {
		if r.Action == "tenant.watch_sources" {
			t.Errorf("audit row for a refused write: %+v", r)
		}
	}
}
