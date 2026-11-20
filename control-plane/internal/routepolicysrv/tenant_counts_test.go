package routepolicysrv

import (
	"context"
	"encoding/json"
	"testing"
)

// TestTenantCounts: the admin Overview's counts are super_admin only, hold numbers and nothing
// else, and count a layer only where it narrows something.
func TestTenantCounts(t *testing.T) {
	st, mux, _ := routePolicyFixture(t, "")
	for _, slug := range []string{"demo", "zeta", "idle"} {
		if _, err := st.CreateTenant(context.Background(), slug, slug); err != nil {
			t.Fatal(err)
		}
	}
	put := func(path, body string) {
		t.Helper()
		if w := routeAdminDo(mux, "PUT", "/api/admin/"+path, "root-x", body); w.Code != 200 {
			t.Fatalf("PUT %s %s: %d %s", path, body, w.Code, w.Body)
		}
	}
	get := func(who string) (int, string) {
		w := routeAdminDo(mux, "GET", "/api/admin/route-policy/tenant-counts", who, "")
		return w.Code, w.Body.String()
	}
	count := func() map[string]any {
		t.Helper()
		code, body := get("root-x")
		if code != 200 {
			t.Fatalf("GET: %d %s", code, body)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	want := func(tenants, caps, own float64) {
		t.Helper()
		m := count()
		if len(m) != 3 || m["tenants"] != tenants || m["withCap"] != caps || m["withOwnRestriction"] != own {
			t.Fatalf("counts = %v, want tenants=%v withCap=%v withOwnRestriction=%v (and no other key)", m, tenants, caps, own)
		}
	}

	want(4, 0, 0) // acme, demo, zeta, idle; nothing stored

	put("tenants/acme/route-policy/cap", `{"deny":["openrouter-api"]}`)
	put("tenants/demo/route-policy/cap", `{"endpoints":["*.corp.example"]}`) // narrows hosts only
	put("tenants/zeta/route-policy/cap", `{"except":["copilot-login"]}`)     // narrows nothing
	put("tenants/acme/route-policy", `{"classes":["provided"]}`)
	put("tenants/demo/route-policy", `{"except":["copilot-login"],"endpoints":["llm.corp.example"]}`) // approves, restricts nothing
	want(4, 2, 1)

	put("tenants/acme/route-policy/cap", `{}`) // cleared: no longer counted
	want(4, 1, 1)

	// Only a super_admin: the tenant's own administrator and a member are refused, the body names no count.
	for _, who := range []string{"boss-x", "m-x"} {
		if code, body := get(who); code != 403 {
			t.Fatalf("%s: %d %s", who, code, body)
		}
	}
	if code, _ := get("nobody"); code != 401 {
		t.Fatalf("unknown caller: %d", code)
	}
}
