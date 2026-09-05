package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// previewHostFactory hands every workspace the same stub runtime pointed at the fake
// Agent, so the relay can be exercised without docker.
type previewHostFactory struct{ endpoint string }

func (f previewHostFactory) New(ws runtime.Workspace, secretKey string, extraEnv []string) runtime.Runtime {
	return previewTestRuntime{endpoint: f.endpoint, token: "tok-" + ws.ID}
}

type previewHostEnv struct {
	mgr    *manager
	cfg    config
	mux    *http.ServeMux // the Console origin (dev auth waves the authGate through)
	host   http.Handler   // the preview host's entry point (dispatch)
	ws     store.Workspace
	domain string
}

func newPreviewHostEnv(t *testing.T, agentURL string) *previewHostEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	dflt, err := st.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatalf("default tenant: %v", err)
	}
	ident, err := st.UpsertIdentity(ctx, "", "preview-user", "")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	mem, err := st.EnsureMembership(ctx, ident.ID, dflt.ID, "member")
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	ws := store.Workspace{ID: "ws-1", TenantID: dflt.ID, MembershipID: mem.ID, ContainerName: "c",
		Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t", State: "running", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	mgr := &manager{
		rts:             map[string]cachedRT{},
		store:           st,
		authMode:        "dev",
		devUser:         "preview-user",
		emailHeader:     "X-Forwarded-Email",
		provisionMode:   "auto",
		defaultTenantID: dflt.ID,
		conns:           newConnRegistry(),
		tenantLogin:     newTenantLoginCache(st),
		rtFactory:       previewHostFactory{endpoint: agentURL},
		previewDomain:   "pv.example.com",
		publicBaseURL:   "https://af.example.com",
	}
	cfg := config{
		mgr:           mgr,
		publicBaseURL: "https://af.example.com",
		cookieSecret:  []byte("test-cookie-secret-0123456789"),
		previewDomain: mgr.previewDomain,
	}
	mux := http.NewServeMux()
	registerTerminalPreviewRoutes(mux, cfg)
	registerAgentEnvRoutes(mux, cfg) // ws-settings (allowed ports, reissue) via the real route table too
	return &previewHostEnv{
		mgr: mgr, cfg: cfg, mux: mux,
		host:   newPreviewHostAPI(cfg).dispatch(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })),
		ws:     ws,
		domain: mgr.previewDomain,
	}
}

// toWSSettingsJSON marshals the settings blob the way the API does.
func toWSSettingsJSON(t *testing.T, st wsSettings) string {
	t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal ws settings: %v", err)
	}
	return string(b)
}

func (e *previewHostEnv) mintSlug(t *testing.T) string {
	t.Helper()
	slug, err := e.mgr.rotatePreviewSlug(context.Background(), e.ws)
	if err != nil {
		t.Fatalf("rotatePreviewSlug: %v", err)
	}
	return slug
}

func (e *previewHostEnv) get(t *testing.T, slug string, port int, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	host := previewHostname(slug, port, e.domain)
	req := httptest.NewRequest(http.MethodGet, "https://"+host+path, nil)
	req.Host = host
	req.Header.Set("Accept", "text/html")
	req.Header.Set("X-Forwarded-Proto", "https")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.host.ServeHTTP(rec, req)
	return rec
}

// TestPreviewHostHandshakeThenProxy drives the docs/log/81 §6 handshake end to end: an
// unauthenticated preview request → 302 to the Console origin → one-time token → a cookie
// scoped to the preview host → the actual relay.
func TestPreviewHostHandshakeThenProxy(t *testing.T) {
	var seen *http.Request
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	defer agent.Close()
	e := newPreviewHostEnv(t, agent.URL)
	slug := e.mintSlug(t)

	// 1) The first hit without a cookie bounces to the Console origin.
	rec := e.get(t, slug, 3000, "/dashboard")
	if rec.Code != http.StatusFound {
		t.Fatalf("first hit: code=%d, want 302 to the Console origin", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://af.example.com"+previewHandshakePath+"?") {
		t.Fatalf("first hit Location=%q, want the Console handshake", loc)
	}

	// 2) The Console origin, where the user is signed in, mints a one-time token and sends
	// the request back.
	rec2 := httptest.NewRecorder()
	e.mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, loc, nil))
	if rec2.Code != http.StatusFound {
		t.Fatalf("handshake: code=%d body=%s", rec2.Code, rec2.Body.String())
	}
	back := rec2.Header().Get("Location")
	if !strings.HasPrefix(back, "https://"+previewHostname(slug, 3000, e.domain)+previewAuthCallbackPath) {
		t.Fatalf("handshake Location=%q, want the preview host callback", back)
	}

	// 3) The callback mints the cookie and returns to the originally requested path.
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, back, nil)
	req3.Host = previewHostname(slug, 3000, e.domain)
	req3.Header.Set("X-Forwarded-Proto", "https")
	e.host.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusFound {
		t.Fatalf("callback: code=%d body=%s", rec3.Code, rec3.Body.String())
	}
	if got := rec3.Header().Get("Location"); got != "/dashboard" {
		t.Fatalf("callback Location=%q, want the originally requested path", got)
	}
	var ck *http.Cookie
	for _, c := range rec3.Result().Cookies() {
		if c.Name == previewAuthCookie {
			ck = c
		}
	}
	if ck == nil {
		t.Fatal("callback minted no af_pv cookie")
	}
	if !ck.HttpOnly || !ck.Secure || ck.Path != "/" {
		t.Fatalf("af_pv cookie must be HttpOnly+Secure at Path=/ (got HttpOnly=%v Secure=%v Path=%q)", ck.HttpOnly, ck.Secure, ck.Path)
	}

	// 4) With the cookie, the request is relayed.
	rec4 := e.get(t, slug, 3000, "/dashboard", ck)
	if rec4.Code != http.StatusOK {
		t.Fatalf("proxied request: code=%d body=%s", rec4.Code, rec4.Body.String())
	}
	if seen == nil {
		t.Fatal("the Agent never saw the request")
	}
	if seen.URL.Path != "/proxy/3000/dashboard" {
		t.Errorf("Agent path=%q, want /proxy/3000/dashboard", seen.URL.Path)
	}
	// The public name goes over X-Forwarded-Host: it is what keeps Next.js Server Actions
	// from answering 403 (decision 9).
	if got := seen.Header.Get("X-Forwarded-Host"); got != previewHostname(slug, 3000, e.domain) {
		t.Errorf("X-Forwarded-Host=%q, want the public preview host", got)
	}
	if got := seen.Header.Get("X-Forwarded-Proto"); got != "https" {
		t.Errorf("X-Forwarded-Proto=%q, want https", got)
	}
	// In host mode the app sits at the root, so no prefix is sent.
	if got := seen.Header.Get("X-Forwarded-Prefix"); got != "" {
		t.Errorf("X-Forwarded-Prefix=%q, want it absent in host mode", got)
	}
	// The preview's admission ticket must never reach the app.
	if strings.Contains(seen.Header.Get("Cookie"), previewAuthCookie) {
		t.Errorf("af_pv leaked to the previewed app: %q", seen.Header.Get("Cookie"))
	}

	// 5) A port that was not allowed does not exist.
	if rec := e.get(t, slug, 5432, "/", ck); rec.Code != http.StatusNotFound {
		t.Errorf("disallowed port: code=%d, want 404", rec.Code)
	}

	// 6) Stopping drops the slug with it, so the previous URL dies (decision 3).
	if err := e.mgr.store.SetWorkspaceState(context.Background(), e.ws.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	if rec := e.get(t, slug, 3000, "/dashboard", ck); rec.Code != http.StatusNotFound {
		t.Errorf("after stop: code=%d, want 404 (the slug must not resolve)", rec.Code)
	}
}

// TestPreviewSlugRotationAndFixedOptIn: the slug is re-minted on every start and dies on
// stop. Only a workspace that opted into a fixed slug comes back to the same URL across a
// stop, which exists so an external IdP's redirect URI can be registered once
// (docs/log/81 §4.1).
func TestPreviewSlugRotationAndFixedOptIn(t *testing.T) {
	e := newPreviewHostEnv(t, "http://127.0.0.1:1")
	ctx := context.Background()

	first := e.mintSlug(t)
	second := e.mintSlug(t)
	if first == second {
		t.Fatal("the slug must be re-minted on every start")
	}
	if err := e.mgr.store.SetWorkspaceState(ctx, e.ws.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	if ws, ok, _ := e.mgr.store.GetWorkspaceByPreviewSlug(ctx, second); ok {
		t.Fatalf("a stopped workspace still resolves by slug (%s)", ws.ID)
	}

	// Opt into a fixed slug.
	raw, _ := e.mgr.store.GetWorkspaceSettings(ctx, e.ws.ID)
	st := parseWSSettings(raw)
	st.PreviewFixedSlug = true
	if err := e.mgr.store.SetWorkspaceSettings(ctx, e.ws.ID, toWSSettingsJSON(t, st)); err != nil {
		t.Fatal(err)
	}
	fixed := e.mintSlug(t)
	if err := e.mgr.store.SetWorkspaceState(ctx, e.ws.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	if again := e.mintSlug(t); again != fixed {
		t.Fatalf("fixed slug changed across a stop: %q → %q", fixed, again)
	}
}

// TestPreviewPublicResetsOnEveryStart pins that public mode always returns to OFF on a
// start (fail-closed, decision 12). Nearly every accident this feature can cause is
// someone forgetting they left it public.
func TestPreviewPublicResetsOnEveryStart(t *testing.T) {
	e := newPreviewHostEnv(t, "http://127.0.0.1:1")
	ctx := context.Background()
	raw, _ := e.mgr.store.GetWorkspaceSettings(ctx, e.ws.ID)
	st := parseWSSettings(raw)
	st.PreviewPublic = true
	if err := e.mgr.store.SetWorkspaceSettings(ctx, e.ws.ID, toWSSettingsJSON(t, st)); err != nil {
		t.Fatal(err)
	}
	e.mintSlug(t)
	raw, _ = e.mgr.store.GetWorkspaceSettings(ctx, e.ws.ID)
	if parseWSSettings(raw).PreviewPublic {
		t.Fatal("public mode survived a container start")
	}
}

// TestPreviewPublicModeServesWithoutSignIn: in public mode the request is relayed with no
// cookie and no handshake, and carries noindex.
func TestPreviewPublicModeServesWithoutSignIn(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer agent.Close()
	e := newPreviewHostEnv(t, agent.URL)
	slug := e.mintSlug(t)
	ctx := context.Background()
	raw, _ := e.mgr.store.GetWorkspaceSettings(ctx, e.ws.ID)
	st := parseWSSettings(raw)
	st.PreviewPublic = true
	if err := e.mgr.store.SetWorkspaceSettings(ctx, e.ws.ID, toWSSettingsJSON(t, st)); err != nil {
		t.Fatal(err)
	}
	rec := e.get(t, slug, 8080, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("public preview: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Robots-Tag"); !strings.Contains(got, "noindex") {
		t.Errorf("X-Robots-Tag=%q, want noindex on a public preview", got)
	}
}

// TestPreviewUnknownSlugIsNotFound: an unknown slug is 404 without asking for sign-in.
// Not hinting that signing in might reveal something is exactly what keeps an outsider
// from probing which slugs exist.
func TestPreviewUnknownSlugIsNotFound(t *testing.T) {
	e := newPreviewHostEnv(t, "http://127.0.0.1:1")
	if rec := e.get(t, "zzzzzzzzzzzzzzzzzzzz", 3000, "/"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown slug: code=%d, want 404", rec.Code)
	}
}

// TestPreviewSiblingOriginOptIn covers the sibling-origin opt-in (docs/log/81 §2.4,
// decision 11). By default the CP adds no CORS at all: allowing cross-origin by default
// would mean any third-party page that knows the URL can reach the preview through the
// user's own browser.
func TestPreviewSiblingOriginOptIn(t *testing.T) {
	var appSaw string
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appSaw = r.Method
		// CORS the app set itself. With the opt-in on, the CP's value replaces it: two
		// headers side by side make the browser ignore both.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusOK)
	}))
	defer agent.Close()
	e := newPreviewHostEnv(t, agent.URL)
	slug := e.mintSlug(t)
	ctx := context.Background()

	// Public mode skips the cookie round trip; only the presence of CORS matters here.
	setPreview := func(mut func(*wsSettings)) {
		raw, _ := e.mgr.store.GetWorkspaceSettings(ctx, e.ws.ID)
		st := parseWSSettings(raw)
		mut(&st)
		if err := e.mgr.store.SetWorkspaceSettings(ctx, e.ws.ID, toWSSettingsJSON(t, st)); err != nil {
			t.Fatal(err)
		}
	}
	setPreview(func(st *wsSettings) { st.PreviewPublic = true })

	sibling := "https://" + previewHostname(slug, 3000, e.domain)
	call := func(method string, hdr map[string]string) *httptest.ResponseRecorder {
		host := previewHostname(slug, 8080, e.domain)
		req := httptest.NewRequest(method, "https://"+host+"/api/orders", nil)
		req.Host = host
		req.Header.Set("X-Forwarded-Proto", "https")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		e.host.ServeHTTP(rec, req)
		return rec
	}

	// Default (OFF): the CP adds nothing, and the app's own `*` passes through untouched.
	rec := call(http.MethodGet, map[string]string{"Origin": sibling})
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("CORS credentials allowed with the opt-in OFF: %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("the app's own CORS header was rewritten with the opt-in OFF: %q", got)
	}

	setPreview(func(st *wsSettings) { st.PreviewCrossOrigin = true })

	// The CP answers the preflight and it never reaches the app: a plain dev server usually
	// answers OPTIONS with 405, and failing there is the hardest kind to diagnose.
	appSaw = ""
	rec = call(http.MethodOptions, map[string]string{
		"Origin":                         sibling,
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "content-type",
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight: code=%d, want 204", rec.Code)
	}
	if appSaw != "" {
		t.Errorf("preflight reached the app as %s", appSaw)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != sibling {
		t.Errorf("preflight ACAO=%q, want the sibling origin", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "content-type" {
		t.Errorf("preflight ACAH=%q", got)
	}

	// The real request carries exactly one CP value; the app's `*` is dropped.
	rec = call(http.MethodGet, map[string]string{"Origin": sibling})
	if got := rec.Header().Values("Access-Control-Allow-Origin"); len(got) != 1 || got[0] != sibling {
		t.Errorf("ACAO=%v, want exactly [%s]", got, sibling)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("ACAC=%q, want true", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Origin") {
		t.Errorf("Vary=%q, want it to include Origin", got)
	}

	// Another workspace's preview is not allowed through: its slug differs.
	rec = call(http.MethodGet, map[string]string{"Origin": "https://zzzzzzzzzzzzzzzzzzzz-3000." + e.domain})
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("a foreign workspace's preview origin was allowed: %q", got)
	}
	// Neither is an origin naming a port that was never allowed.
	rec = call(http.MethodGet, map[string]string{"Origin": "https://" + previewHostname(slug, 5432, e.domain)})
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("a non-allowlisted port was accepted as an origin: %q", got)
	}
}

// TestPreviewReissueKillsTheCurrentURL covers reissue (docs/log/81 §4.1): a URL that has
// already been handed out can be discarded on the spot.
func TestPreviewReissueKillsTheCurrentURL(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer agent.Close()
	e := newPreviewHostEnv(t, agent.URL)
	ctx := context.Background()
	// Reissue with the fixed-slug reservation in place: unless the reservation is discarded
	// too, the supposedly discarded URL comes back at the next start.
	raw, _ := e.mgr.store.GetWorkspaceSettings(ctx, e.ws.ID)
	st := parseWSSettings(raw)
	st.PreviewFixedSlug = true
	if err := e.mgr.store.SetWorkspaceSettings(ctx, e.ws.ID, toWSSettingsJSON(t, st)); err != nil {
		t.Fatal(err)
	}
	old := e.mintSlug(t)
	// Public mode has to be set AFTER the start: it is reset to OFF on every start
	// (fail-closed), so setting it earlier would be wiped. It only stands in for the auth
	// round trip, which is not what this test is about.
	raw, _ = e.mgr.store.GetWorkspaceSettings(ctx, e.ws.ID)
	st = parseWSSettings(raw)
	st.PreviewPublic = true
	if err := e.mgr.store.SetWorkspaceSettings(ctx, e.ws.ID, toWSSettingsJSON(t, st)); err != nil {
		t.Fatal(err)
	}
	if rec := e.get(t, old, 3000, "/"); rec.Code != http.StatusOK {
		t.Fatalf("before reissue: code=%d, want 200", rec.Code)
	}

	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/env/ws-settings/preview/reissue", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("reissue: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec2 := e.get(t, old, 3000, "/"); rec2.Code != http.StatusNotFound {
		t.Fatalf("after reissue the old URL still resolves: code=%d", rec2.Code)
	}
	ws, ok, err := e.mgr.store.GetWorkspaceByMembership(ctx, e.ws.MembershipID)
	if err != nil || !ok || ws.PreviewSlug == "" || ws.PreviewSlug == old {
		t.Fatalf("reissue did not mint a new slug (got %q, old %q)", ws.PreviewSlug, old)
	}
	raw, _ = e.mgr.store.GetWorkspaceSettings(ctx, e.ws.ID)
	if parseWSSettings(raw).PreviewReservedSlug == old {
		t.Fatal("the discarded slug is still reserved — it would come back on the next start")
	}
	// The response has to say that a new slug was actually minted: the Console picks its
	// wording from this, so dropping it makes a success look like a dead button — which is
	// how it was reported.
	if !decodeReissued(t, rec) {
		t.Error("previewReissued=false although a new slug was minted")
	}
}

// TestPreviewReissueOnStoppedWorkspaceSaysNothingHappened: reissuing while stopped (no slug
// issued) succeeds but does nothing. The response must carry that distinction, or the
// Console cannot tell a success apart from a no-op.
func TestPreviewReissueOnStoppedWorkspaceSaysNothingHappened(t *testing.T) {
	e := newPreviewHostEnv(t, "http://127.0.0.1:1")
	ctx := context.Background()
	e.mintSlug(t)
	// Stopping expires the slug.
	if err := e.mgr.store.SetWorkspacePreviewSlug(ctx, e.ws.ID, ""); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/env/ws-settings/preview/reissue", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("reissue while stopped: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if decodeReissued(t, rec) {
		t.Error("previewReissued=true although the workspace had no slug to discard")
	}
}

func decodeReissued(t *testing.T, rec *httptest.ResponseRecorder) bool {
	t.Helper()
	var body struct {
		PreviewReissued bool `json:"previewReissued"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode reissue response: %v (%s)", err, rec.Body.String())
	}
	return body.PreviewReissued
}
