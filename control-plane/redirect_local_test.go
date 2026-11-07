package main

import (
	"context"
	"encoding/json"
	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
	"net/http"
	"net/http/httptest"
	"strings"
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

// The mux matches the decoded segment, so an encoded spelling of the legacy
// prefix must be stripped too, not bounce to itself.
func TestLegacyRedirectStripsEncodedPrefix(t *testing.T) {
	mux := http.NewServeMux()
	registerLegacyRedirect(mux)
	for _, tc := range []struct{ target, want string }{
		{"/agent-%66leet/sessions?tenant=a", "/sessions?tenant=a"},
		{"/%61gent-fleet/a%2Fb", "/a%2Fb"},
		{"/agent%2Dfleet", "/"},
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.target, nil))
		if got := w.Header().Get("Location"); got != tc.want {
			t.Errorf("%s: Location = %q, want %q", tc.target, got, tc.want)
		}
	}
}

// Sign-in state must not depend on whether the tenant exists (the cookie is
// readable); the hint is dropped after authentication instead.
func TestLoginStateDoesNotRevealTenantExistence(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{
		idTokenClaims:  map[string]any{"sub": "g1", "email": "yamada@acme.co.jp", "email_verified": true},
		userinfoClaims: map[string]any{"sub": "g1", "email": "yamada@acme.co.jp", "email_verified": true},
	})
	cfg := oauthTestConfig(t, stubProvider("okta", idp, auth.TrustEmailVerified))
	st, _ := startLogin(t, cfg, "?provider=okta&tenant=ghost")
	payload, ok := cfg.verifyCookie(st.Value)
	if !ok {
		t.Fatal("state cookie does not verify")
	}
	var s oauthState
	if err := json.Unmarshal(payload, &s); err != nil {
		t.Fatal(err)
	}
	if s.Tnt != "ghost" {
		t.Fatalf("state Tnt = %q: an unknown slug must be kept so the cookie is the same either way", s.Tnt)
	}
	w := callback(t, cfg, st, "code", s.Nonce)
	if sessionCookieOf(t, w) == nil {
		t.Fatalf("no session (error=%q)", loginErrorOf(t, w))
	}
	if loc := w.Header().Get("Location"); strings.Contains(loc, "tenant=") {
		t.Fatalf("unknown tenant leaked into the post-login URL: %q", loc)
	}
}
