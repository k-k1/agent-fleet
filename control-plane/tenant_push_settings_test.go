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
	for _, email := range []string{"user@sub.co.jp", "admin@other.co.jp"} {
		if w := pushSettingsCall(mgr, http.MethodGet, "sub", email, ""); w.Code < 400 {
			t.Fatalf("%s read the setting: %d", email, w.Code)
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

// Only the release route is reachable without a session. Run through authGate with a real mux, so
// dropping the exemption (or exempting a sibling) turns this red.
func TestPushRoutesAuthBoundary(t *testing.T) {
	t.Setenv("AF_WEB_PUSH", "true")
	cfg, _ := smokeEnvWith(t, allRouteSwitches(t)...)
	h := cfg.authGate(buildMux(cfg))
	code := func(method, path string) int {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		r.Header.Set("Accept", "application/json")
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	// The service is not configured in this fixture, so an exempt route answers 503 from the
	// handler, while a gated one stops at 401 before it.
	if c := code("POST", "/api/push/release"); c != http.StatusServiceUnavailable {
		t.Fatalf("release without a session: %d, want the handler's 503", c)
	}
	for _, rt := range [][2]string{{"GET", "/api/push/prefs"}, {"PUT", "/api/push/prefs"}, {"POST", "/api/push/viewing"},
		{"GET", "/api/push/subscriptions"}, {"GET", "/api/admin/tenants/sub/push-settings"}, {"PUT", "/api/admin/tenants/sub/push-settings"}} {
		if c := code(rt[0], rt[1]); c != http.StatusUnauthorized {
			t.Fatalf("%s %s without a session: %d, want 401", rt[0], rt[1], c)
		}
	}
}
