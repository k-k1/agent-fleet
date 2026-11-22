package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func pushSettingsCall(mgr *manager, method, slug, email, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/admin/tenants/"+slug+"/push-settings", strings.NewReader(body))
	r.SetPathValue("slug", slug)
	r.Header.Set("X-Forwarded-Email", email)
	w := httptest.NewRecorder()
	adm := newAdminAPI(mgr)
	if method == http.MethodGet {
		adm.tenantPushSettings(w, r)
	} else {
		adm.setTenantPushSettings(w, r)
	}
	return w
}

func TestTenantPushSettings(t *testing.T) {
	st := p3Store(t)
	mgr := p3Manager(t, st)
	sub := seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")
	seedGitOAuthTenant(t, st, "other", "admin@other.co.jp")
	addBranchRulesMember(t, st, sub.ID, "user@sub.co.jp", "member")

	if w := pushSettingsCall(mgr, http.MethodGet, "sub", "admin@sub.co.jp", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"includeText":false`) {
		t.Fatalf("default must be off: %d %s", w.Code, w.Body)
	}
	if w := pushSettingsCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", `{"includeText":true}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := pushSettingsCall(mgr, http.MethodGet, "sub", "admin@sub.co.jp", ""); !strings.Contains(w.Body.String(), `"includeText":true`) {
		t.Fatalf("not stored: %s", w.Body)
	}
	// Not the tenant's admin: a member, or another tenant's admin, can neither read nor write.
	for _, email := range []string{"user@sub.co.jp", "admin@other.co.jp"} {
		if w := pushSettingsCall(mgr, http.MethodPut, "sub", email, `{"includeText":false}`); w.Code < 400 {
			t.Fatalf("%s wrote the setting: %d", email, w.Code)
		}
	}
	if w := pushSettingsCall(mgr, http.MethodGet, "other", "admin@other.co.jp", ""); !strings.Contains(w.Body.String(), `"includeText":false`) {
		t.Fatalf("one tenant's setting leaked to another: %s", w.Body)
	}
	for _, bad := range []string{`{}`, `{"includeText":"yes"}`, `{"includeText":true,"x":1}`, `{`} {
		if w := pushSettingsCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", bad); w.Code != 400 {
			t.Fatalf("accepted %s: %d", bad, w.Code)
		}
	}
}
