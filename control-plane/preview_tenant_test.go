package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// previewTestRuntime is a minimal Runtime stub whose Token() lets the fake Agent
// below report back which membership's runtime actually served a request.
type previewTestRuntime struct {
	endpoint, token string
}

func (r previewTestRuntime) Start(context.Context) error  { return nil }
func (r previewTestRuntime) Stop(context.Context) error   { return nil }
func (r previewTestRuntime) State(context.Context) string { return "running" }
func (r previewTestRuntime) Endpoint() string             { return r.endpoint }
func (r previewTestRuntime) Token() string                { return r.token }
func (r previewTestRuntime) Name() string                 { return "preview-test" }

// newPreviewTenantTestEnv wires the real /preview/{port} route table
// (registerTerminalPreviewRoutes) to one identity that belongs to TWO tenants —
// the ambiguous case selectMembership must gate — each pre-seeded into
// mgr.rts so no real workspace/docker build is involved. Every request the fake
// Agent receives echoes back its own Authorization bearer, letting assertions
// tell which tenant's runtime actually served it.
func newPreviewTenantTestEnv(t *testing.T, agentURL string) (mux *http.ServeMux) {
	t.Helper()
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	dflt, err := st.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatalf("default tenant: %v", err)
	}
	sec, err := st.CreateTenant(ctx, "security", "Security")
	if err != nil {
		t.Fatalf("security tenant: %v", err)
	}
	ident, err := st.UpsertIdentity(ctx, "", "preview-user", "")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	memDefault, err := st.EnsureMembership(ctx, ident.ID, dflt.ID, "member")
	if err != nil {
		t.Fatalf("membership default: %v", err)
	}
	memSecurity, err := st.EnsureMembership(ctx, ident.ID, sec.ID, "member")
	if err != nil {
		t.Fatalf("membership security: %v", err)
	}

	mgr := &manager{
		rts: map[string]cachedRT{
			memDefault.ID:  {rt: previewTestRuntime{endpoint: agentURL, token: "tok-default"}, ws: Workspace{ID: "ws-default", TenantID: dflt.ID, MembershipID: memDefault.ID}},
			memSecurity.ID: {rt: previewTestRuntime{endpoint: agentURL, token: "tok-security"}, ws: Workspace{ID: "ws-security", TenantID: sec.ID, MembershipID: memSecurity.ID}},
		},
		store:           st,
		authMode:        "dev",
		devUser:         "preview-user",
		provisionMode:   "auto",
		defaultTenantID: dflt.ID,
		conns:           newConnRegistry(),
	}
	mux = http.NewServeMux()
	registerTerminalPreviewRoutes(mux, config{mgr: mgr})
	return mux
}

// previewCookie extracts the af_pv_tenant cookie a response minted, failing the
// test if none was set.
func previewCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == previewTenantCookie {
			return c
		}
	}
	t.Fatalf("no %s cookie in response (Set-Cookie=%v)", previewTenantCookie, rec.Header().Values("Set-Cookie"))
	return nil
}

// TestPreviewTenantCookieCarriesAcrossSubRequests is the docs scenario end to
// end: the first navigation resolves the tenant via ?tenant=, and the page's own
// follow-up requests for style.css and api/example — neither carrying the query
// — must resolve to the SAME tenant via the cookie that first request minted.
func TestPreviewTenantCookieCarriesAcrossSubRequests(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen-Auth", r.Header.Get("Authorization"))
		w.Header().Set("X-Seen-Prefix", r.Header.Get("X-Forwarded-Prefix"))
		w.WriteHeader(http.StatusOK)
	}))
	defer agent.Close()
	mux := newPreviewTenantTestEnv(t, agent.URL)

	// 1) GET /preview/{port}/?tenant=default succeeds and mints the cookie.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/preview/8080/?tenant=default", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("initial html: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Seen-Auth"); got != "Bearer tok-default" {
		t.Fatalf("initial html routed to wrong runtime: Authorization=%q", got)
	}
	ck := previewCookie(t, rec)
	if ck.Value != "default" {
		t.Fatalf("cookie value=%q, want %q", ck.Value, "default")
	}
	if want := "/preview/8080/"; ck.Path != want {
		t.Fatalf("cookie Path=%q, want %q", ck.Path, want)
	}
	if !ck.HttpOnly {
		t.Fatal("cookie must be HttpOnly")
	}

	// 2) GET /preview/{port}/style.css, no ?tenant=, only the minted cookie —
	// same tenant, same runtime.
	req := httptest.NewRequest(http.MethodGet, "/preview/8080/style.css", nil)
	req.AddCookie(ck)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("style.css: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Seen-Auth"); got != "Bearer tok-default" {
		t.Fatalf("style.css routed to wrong runtime: Authorization=%q", got)
	}
	if got := rec.Header().Get("X-Seen-Prefix"); got != "/preview/8080" {
		t.Fatalf("style.css X-Forwarded-Prefix=%q", got)
	}

	// 3) GET /preview/{port}/api/example, same story for the app's own API calls.
	req = httptest.NewRequest(http.MethodGet, "/preview/8080/api/example", nil)
	req.AddCookie(ck)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("api/example: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Seen-Auth"); got != "Bearer tok-default" {
		t.Fatalf("api/example routed to wrong runtime: Authorization=%q", got)
	}
}

// TestPreviewTenantCookieDoesNotOverrideExplicitTenant verifies an explicit
// ?tenant= always wins over a stale cookie from a different tenant, and that a
// switch mints a fresh cookie for the newly selected tenant.
func TestPreviewTenantCookieDoesNotOverrideExplicitTenant(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen-Auth", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer agent.Close()
	mux := newPreviewTenantTestEnv(t, agent.URL)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/preview/8080/?tenant=default", nil))
	defaultCookie := previewCookie(t, rec)

	// A follow-up request that carries the "default" cookie AND an explicit
	// ?tenant=security query must resolve as security — explicit selection wins.
	req := httptest.NewRequest(http.MethodGet, "/preview/8080/?tenant=security", nil)
	req.AddCookie(defaultCookie)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("explicit switch: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Seen-Auth"); got != "Bearer tok-security" {
		t.Fatalf("explicit ?tenant=security must win over the stale cookie, got Authorization=%q", got)
	}
	securityCookie := previewCookie(t, rec)
	if securityCookie.Value != "security" {
		t.Fatalf("re-minted cookie value=%q, want %q", securityCookie.Value, "security")
	}
}

// TestPreviewTenantCookieNotForwardedToApp verifies AF's own tenant-resolution
// mechanism (the af_pv_tenant cookie) never reaches the previewed app's backend —
// the app must stay as ignorant of it as it already is of ?tenant=.
func TestPreviewTenantCookieNotForwardedToApp(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen-Cookie", r.Header.Get("Cookie"))
		w.WriteHeader(http.StatusOK)
	}))
	defer agent.Close()
	mux := newPreviewTenantTestEnv(t, agent.URL)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/preview/8080/?tenant=default", nil))
	ck := previewCookie(t, rec)

	req := httptest.NewRequest(http.MethodGet, "/preview/8080/style.css", nil)
	req.AddCookie(ck)
	req.AddCookie(&http.Cookie{Name: "app_own_cookie", Value: "keep-me"})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	seen := rec.Header().Get("X-Seen-Cookie")
	if strings.Contains(seen, previewTenantCookie) {
		t.Fatalf("af_pv_tenant leaked to the previewed app: Cookie=%q", seen)
	}
	if !strings.Contains(seen, "app_own_cookie=keep-me") {
		t.Fatalf("app's own cookie must still pass through: Cookie=%q", seen)
	}
}

// TestPreviewTenantUnresolvedFailsSafe covers the two failure modes required
// alongside the cookie carry-forward: no tenant at all (ambiguous — this identity
// belongs to two tenants) must still 409, and a tampered/mismatched cookie value
// must still 403 rather than silently falling back to some other membership.
func TestPreviewTenantUnresolvedFailsSafe(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer agent.Close()
	mux := newPreviewTenantTestEnv(t, agent.URL)

	// No ?tenant= and no cookie: ambiguous membership selection must 409, not
	// silently pick a tenant.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/preview/8080/style.css", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("no tenant: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// A cookie naming a tenant this identity isn't a member of must 403, not
	// silently cross to a membership the cookie didn't legitimately name.
	req := httptest.NewRequest(http.MethodGet, "/preview/8080/style.css", nil)
	req.AddCookie(&http.Cookie{Name: previewTenantCookie, Value: "not-a-real-tenant"})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bogus cookie tenant: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
