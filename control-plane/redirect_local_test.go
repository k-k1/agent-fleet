package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSafePreviewNextUsesTheSameCheck(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/a/b?c=d", "/a/b?c=d"},
		{"/a\tb", "/"},
		{`/a\b`, "/"},
		{"/%5Cevil.example", "/"},
		{previewAuthCallbackPath + "?next=/x", "/"},
	} {
		if got := safePreviewNext(tc.in); got != tc.want {
			t.Errorf("safePreviewNext(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLegacyRedirectOnlyEmitsLocalPaths(t *testing.T) {
	mux := http.NewServeMux()
	registerLegacyRedirect(mux)
	for _, tc := range []struct{ target, want string }{
		{"/agent-fleet", "/"},
		{"/agent-fleet/sessions?tenant=a", "/sessions?tenant=a"},
		{"/agent-fleet//evil.example", "/agent-fleet/evil.example"}, // the mux cleans the path itself
		{"/agent-fleet/%2Fevil.example", "/"},
		{"/agent-fleet/%5Cevil.example", "/"},
		{"/agent-fleet/a%2Fb", "/a%2Fb"},
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.target, nil))
		if got := w.Header().Get("Location"); got != tc.want {
			t.Errorf("%s: Location = %q, want %q", tc.target, got, tc.want)
		}
	}
}

func TestTenantExistsOnlyForRealTenants(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	if _, err := st.CreateTenant(ctx, "alpha", "A"); err != nil {
		t.Fatal(err)
	}
	c := config{mgr: mgr}
	if !c.tenantExists(ctx, "alpha") {
		t.Error("existing tenant not recognised")
	}
	if c.tenantExists(ctx, "nosuch") {
		t.Error("unknown slug accepted as a tenant hint")
	}
	if (config{}).tenantExists(ctx, "alpha") {
		t.Error("no store must answer false")
	}
}
