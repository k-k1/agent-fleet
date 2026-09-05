// preview_host_serve.go — the host-mode preview path (docs/log/81 §6, §7 / ADR 0062).
//
//	browser → {slug}-{port}.{AF_PREVIEW_DOMAIN} → CP (this layer) → Agent /proxy/{port}/… → 127.0.0.1:{port}
//
// This layer sits OUTSIDE authGate and outside gzip / etag (decision 8): a preview host has
// its own authentication (the handshake below), separate from the Console's, and what it
// serves is not the CP's JSON.
//
// A preview host exposes neither the CP API nor the Console. It answers only
// __af/preview-auth and the proxy itself; everything else is a 404. One process holds both,
// so loosening this re-opens from the back door exactly what the separate origin closed:
// reaching the CP from the preview's origin.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// previewAuthCookie is minted per PREVIEW HOST (host-only, Path=/). Never the
// Console's session cookie: the previewed app is arbitrary user code, and a browser
// that would send af_session to it hands it the signed-in person's API access.
const previewAuthCookie = "af_pv"

// previewAuthCallbackPath is the one path the preview host answers besides the proxy.
// Under /__af/ so it cannot collide with a real app route by accident.
const previewAuthCallbackPath = "/__af/preview-auth"

// previewHandshakePath is the Console-origin half of the handshake (inside authGate,
// so an unauthenticated visitor lands on the normal login first).
const previewHandshakePath = "/preview-auth"

// previewOpenPath is the STABLE link people paste (docs/log/81 §14.6 / ADR 0062 decision 17).
// The preview hostname changes at every workspace start, so a raw URL always rots —
// and it rots as a 404, the least legible failure there is. This one names the owner
// and the port instead, and resolves the current slug on each visit.
const previewOpenPath = "/preview-open"

// previewTokenTTL is how long the one-shot token minted on the Console origin stays
// valid — it only has to survive one redirect.
const previewTokenTTL = 30 * time.Second

// previewSessionTTL is the preview host cookie's lifetime. Deliberately shorter than
// the Console session: this cookie lives on an origin running the user's own code.
const previewSessionTTL = 12 * time.Hour

// previewClaims is both the one-shot token and the cookie payload. The slug is IN the
// signature on purpose: restarting the workspace mints a new slug, which invalidates
// every cookie issued for the previous one — no separate revocation path.
type previewClaims struct {
	Slug         string `json:"slug"`
	Port         int    `json:"port"`
	MembershipID string `json:"ms"`
	Exp          int64  `json:"exp"`
}

// previewHostAPI serves everything that arrives on a preview hostname.
type previewHostAPI struct {
	cfg config
	mgr *manager
}

func newPreviewHostAPI(cfg config) previewHostAPI { return previewHostAPI{cfg: cfg, mgr: cfg.mgr} }

// dispatch wraps the whole CP handler chain. A request whose Host does not parse as a
// preview hostname passes through untouched — including the ALB health check (whose
// Host is the task IP) and every Console request.
func (a previewHostAPI) dispatch(next http.Handler) http.Handler {
	if a.mgr == nil || a.mgr.previewDomain == "" {
		return next // host-mode preview not configured: nothing changes
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ph, ok := parsePreviewHost(r.Host, a.mgr.previewDomain)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		a.serve(w, r, ph)
	})
}

func (a previewHostAPI) serve(w http.ResponseWriter, r *http.Request, ph previewHost) {
	ctx := r.Context()
	ws, ok, err := a.mgr.store.GetWorkspaceByPreviewSlug(ctx, ph.slug)
	if err != nil {
		http.Error(w, "preview lookup failed", http.StatusInternalServerError)
		return
	}
	// A stopped workspace has no slug, so it fails here. Do not answer "it is stopped":
	// a slug that exists must be indistinguishable from one that does not.
	if !ok {
		previewNotFound(w)
		return
	}
	st := parseWSSettings(mustSettings(ctx, a.mgr, ws.ID))
	if !previewPortAllowed(st, ph.port) {
		previewNotFound(w) // a port off the allowlist "does not exist" (decision 6)
		return
	}
	// The tenant's network restriction applies even in public mode (decision 12): if a
	// tenant has narrowed its network, its previews are on the narrowed side too.
	if !a.mgr.tenantLogin.networkAllowed(ctx, ws.TenantID, clientIPFrom(ctx)) {
		http.Error(w, "not allowed from this network", http.StatusForbidden)
		return
	}
	if r.URL.Path == previewAuthCallbackPath {
		a.acceptToken(w, r, ph, st)
		return
	}
	// Calls from a sibling origin — the same slug on another port (docs/log/81 §2.4,
	// decision 11). Only when opted in does the CP answer the preflight and add CORS.
	allowOrigin := a.siblingOrigin(r, ph, st)
	if allowOrigin != "" && r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
		// Never pass the preflight through to the app. A plain dev server or Spring
		// normally answers OPTIONS with 403 / 405, and failing there reads as "CORS is
		// allowed and still does not work".
		writePreviewCORS(w.Header(), allowOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
		if h := r.Header.Get("Access-Control-Request-Headers"); h != "" {
			w.Header().Set("Access-Control-Allow-Headers", h)
		}
		w.Header().Set("Access-Control-Max-Age", "600")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !st.PreviewPublic && !a.authorized(ctx, r, ph, ws, st) {
		a.startHandshake(w, r, ph)
		return
	}
	if err := a.mgr.touchWorkspace(ctx, ws.ID); err != nil {
		http.Error(w, "workspace unavailable", http.StatusServiceUnavailable)
		return
	}
	if unsafeRelayPath(strings.TrimPrefix(r.URL.Path, "/")) {
		http.Error(w, "bad preview path", http.StatusBadRequest)
		return
	}
	rt := a.mgr.runtimeFor(ws, "")
	opts := previewRelayOptions{
		port:           ph.port,
		path:           r.URL.Path,
		publicHost:     r.Host,
		proto:          forwardedProto(r),
		public:         st.PreviewPublic,
		identityHeader: a.mgr.emailHeader,
		allowOrigin:    allowOrigin,
	}
	relayPreview(w, r, rt, opts)
}

// previewNotFound answers everything we refuse with the same bare 404 — a wrong slug,
// a stopped workspace and a port that is not on the allowlist must be indistinguishable
// from outside.
func previewNotFound(w http.ResponseWriter) {
	http.Error(w, "not found", http.StatusNotFound)
}

// mustSettings reads a workspace's settings blob and falls back to "" (= no overrides)
// when the store cannot answer, because refusing to serve a preview over a settings
// read is worse than serving it with the defaults.
//
// It must never fall back silently: while `workspace.settings` was missing from the Postgres
// migration, swallowing the error reduced the symptom to "every setting looks like its
// default" and nothing surfaced until someone hit a 500 on a write. Keep the fallback, keep
// the trace.
func mustSettings(ctx context.Context, m *manager, wsID string) string {
	raw, err := m.store.GetWorkspaceSettings(ctx, wsID)
	if err != nil {
		log.Printf("workspace settings unreadable (ws=%s): %v — falling back to defaults", wsID, err)
		return ""
	}
	return raw
}

// authorized reports whether this request already carries a valid preview cookie for
// THIS host (slug + port both checked — a cookie is host-scoped by the browser, but we
// do not rely on that alone) AND whether the person it names may still open it.
//
// A cookie can only say WHO; whether they may look is re-resolved on every request
// (previewViewerAllowed / ADR 0062 decision 15). The cookie lives 12 hours, so baking the
// permission into it would keep it alive after sharing is switched off or the person is
// removed from the tenant.
func (a previewHostAPI) authorized(ctx context.Context, r *http.Request, ph previewHost, ws store.Workspace, st wsSettings) bool {
	c, err := r.Cookie(previewAuthCookie)
	if err != nil || c.Value == "" {
		return false
	}
	cl, ok := a.verifyClaims(c.Value)
	if !ok {
		return false
	}
	if cl.Slug != ph.slug || cl.Port != ph.port {
		return false
	}
	return previewViewerAllowed(ctx, a.mgr, ws, st, cl.MembershipID)
}

func (a previewHostAPI) verifyClaims(s string) (previewClaims, bool) {
	payload, ok := a.cfg.verifyCookie(s)
	if !ok {
		return previewClaims{}, false
	}
	var cl previewClaims
	if err := json.Unmarshal(payload, &cl); err != nil {
		return previewClaims{}, false
	}
	if cl.Exp <= time.Now().Unix() {
		return previewClaims{}, false
	}
	return cl, true
}

func (a previewHostAPI) sign(cl previewClaims) string {
	b, _ := json.Marshal(cl)
	return a.cfg.signCookie(b)
}

// startHandshake bounces an unauthenticated preview request to the Console origin,
// which knows how to log someone in. Only a top-level navigation is bounced: a
// sub-resource (fetch/XHR) would follow the redirect into the Console's HTML and
// report a baffling parse error, so those get a plain 401.
func (a previewHostAPI) startHandshake(w http.ResponseWriter, r *http.Request, ph previewHost) {
	if !wantsHTML(r) || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		http.Error(w, "preview requires sign-in", http.StatusUnauthorized)
		return
	}
	base := strings.TrimRight(a.cfg.publicBaseURL, "/")
	if base == "" {
		http.Error(w, "preview auth is not configured (PUBLIC_BASE_URL)", http.StatusServiceUnavailable)
		return
	}
	q := url.Values{}
	q.Set("slug", ph.slug)
	q.Set("port", strconv.Itoa(ph.port))
	q.Set("next", safePreviewNext(r.URL.RequestURI()))
	http.Redirect(w, r, base+previewHandshakePath+"?"+q.Encode(), http.StatusFound)
}

// acceptToken is the preview-origin end of the handshake: verify the one-shot token,
// mint the host-scoped cookie, and send the browser to where it was going.
func (a previewHostAPI) acceptToken(w http.ResponseWriter, r *http.Request, ph previewHost, st wsSettings) {
	cl, ok := a.verifyClaims(r.URL.Query().Get("t"))
	if !ok || cl.Slug != ph.slug || cl.Port != ph.port {
		http.Error(w, "preview sign-in expired — reload from the Console", http.StatusForbidden)
		return
	}
	cl.Exp = time.Now().Add(previewSessionTTL).Unix()
	http.SetCookie(w, &http.Cookie{
		Name:     previewAuthCookie,
		Value:    a.sign(cl),
		Path:     "/",
		HttpOnly: true,
		Secure:   forwardedProto(r) == "https",
		// Lax: a top-level navigation carries it, a cross-site sub-request does not.
		// None is the opt-in for apps that call a sibling preview origin directly
		// (docs/log/81 §2.4) — not the default, because it also means any third-party page
		// that knows the URL can drive the preview with the user's browser.
		SameSite: previewCookieSameSite(st),
		MaxAge:   int(previewSessionTTL.Seconds()),
	})
	http.Redirect(w, r, safePreviewNext(r.URL.Query().Get("next")), http.StatusFound)
}

// previewCookieSameSite picks Lax (default) or None (cross-origin opt-in).
func previewCookieSameSite(st wsSettings) http.SameSite {
	if st.PreviewCrossOrigin {
		return http.SameSiteNoneMode
	}
	return http.SameSiteLaxMode
}

// siblingOrigin returns the Origin to allow for this request, or "" for "no CORS from
// us". It allows exactly one shape: the opt-in is on, and the caller is a preview host
// of the SAME workspace start (same slug) on a port that workspace also allows.
//
// Requiring the slug to match is what keeps another person's workspace preview from ever
// getting through. The port allowlist is checked too, so that a port taken off the list
// cannot still be usable "as a caller".
func (a previewHostAPI) siblingOrigin(r *http.Request, ph previewHost, st wsSettings) string {
	if !st.PreviewCrossOrigin {
		return ""
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return ""
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return ""
	}
	other, ok := parsePreviewHost(u.Host, a.mgr.previewDomain)
	if !ok || other.slug != ph.slug || !previewPortAllowed(st, other.port) {
		return ""
	}
	return origin
}

// writePreviewCORS sets the pair that has to travel together: naming the origin (never
// `*`, which cannot carry credentials) and allowing credentials, plus Vary so a cache
// never serves one origin's answer to another.
func writePreviewCORS(h http.Header, origin string) {
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Allow-Credentials", "true")
	h.Add("Vary", "Origin")
}

// safePreviewNext keeps the post-handshake destination on the preview host: a
// same-origin absolute path, never a scheme or a protocol-relative URL.
func safePreviewNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") ||
		strings.HasPrefix(next, "/\\") || next == previewAuthCallbackPath {
		return "/"
	}
	return next
}

// --- Console-origin half (registered on the normal mux, inside authGate) ---------

// handshake (GET /preview-auth) runs on the CONSOLE origin, where the person is (or
// can be) signed in. It re-resolves them exactly like any other API call — identity,
// membership, tenant provider and tenant network are all re-checked — and only then
// mints a 30-second token for the preview host.
func (a previewHostAPI) handshake(w http.ResponseWriter, r *http.Request) {
	slug := r.URL.Query().Get("slug")
	port, _ := strconv.Atoi(r.URL.Query().Get("port"))
	if !validPreviewSlug(slug) || port < 1 || port > 65535 {
		http.Error(w, "bad preview request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	ws, ok, err := a.mgr.store.GetWorkspaceByPreviewSlug(ctx, slug)
	if err != nil {
		http.Error(w, "preview lookup failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		previewNotFound(w)
		return
	}
	id := a.mgr.resolveIdentity(r)
	if id.key == "" {
		writeAPIErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "no gateway identity"})
		return
	}
	// Resolve the caller within the workspace's OWN tenant. Getting past this proves
	// they are an ACTIVE member of that tenant, with the required provider and from an
	// allowed network — the same three checks every normal API call makes, because it
	// is the same resolver.
	//
	// resolveMembership, not resolveFull: handing the membership to previewViewerAllowed is
	// enough, and there is no reason to build the other side's runtime for a viewer.
	_, mv, aerr := a.mgr.resolveMembership(ctx, id.key, id.email, ws.TenantID)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	st := parseWSSettings(mustSettings(ctx, a.mgr, ws.ID))
	// Either the owner themselves, or a workspace shared with the same tenant
	// (docs/log/81 §14.3). Neither one gets the same 404 as an unknown slug: saying "it
	// exists but belongs to someone else" is itself information about that workspace.
	if !previewViewerAllowed(ctx, a.mgr, ws, st, mv.MembershipID) {
		previewNotFound(w)
		return
	}
	if !previewPortAllowed(st, port) {
		previewNotFound(w)
		return
	}
	// What gets baked in is the CALLER's own membership, never the owner's. A viewer's
	// cookie says only "this person is opening this slug/port"; the permission is
	// re-resolved on every request (decision 15).
	tok := a.sign(previewClaims{
		Slug: slug, Port: port, MembershipID: mv.MembershipID,
		Exp: time.Now().Add(previewTokenTTL).Unix(),
	})
	q := url.Values{}
	q.Set("t", tok)
	q.Set("next", safePreviewNext(r.URL.Query().Get("next")))
	http.Redirect(w, r,
		previewURLFor(slug, port, a.mgr.previewDomain)+previewAuthCallbackPath+"?"+q.Encode(),
		http.StatusFound)
}

// openStable (GET /preview-open?owner={userKey}&port={n}[&next=/path]) runs on the
// CONSOLE origin and answers "where is that person's preview right now?".
//
// No authentication decision lives here: once the ACL passes, it fills in the CURRENT slug
// and hands off to /preview-auth, so tokens are minted in exactly one place (decision 17).
// It sits inside authGate, so a signed-out visitor meets the normal login first.
func (a previewHostAPI) openStable(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	port, _ := strconv.Atoi(r.URL.Query().Get("port"))
	if owner == "" || port < 1 || port > 65535 {
		http.Error(w, "bad preview request", http.StatusBadRequest)
		return
	}
	id := a.mgr.resolveIdentity(r)
	if id.key == "" {
		writeAPIErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "no gateway identity"})
		return
	}
	// Resolve in the caller's CURRENT tenant (no workspace is built). The owner is only
	// looked for inside it, which makes pointing at someone across tenants impossible here.
	_, mv, aerr := a.mgr.resolveMembership(ctx, id.key, id.email, tenantSel(r))
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	member, ok, err := memberByUserKey(ctx, a.mgr.store, mv.TenantID, owner)
	if err != nil {
		http.Error(w, "preview lookup failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		previewNotFound(w)
		return
	}
	ws, ok, err := a.mgr.store.GetWorkspaceByMembership(ctx, member.MembershipID)
	if err != nil {
		http.Error(w, "preview lookup failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		previewNotFound(w)
		return
	}
	st := parseWSSettings(mustSettings(ctx, a.mgr, ws.ID))
	if !previewViewerAllowed(ctx, a.mgr, ws, st, mv.MembershipID) || !previewPortAllowed(st, port) {
		previewNotFound(w)
		return
	}
	if ws.PreviewSlug == "" {
		// This is the one place that may answer "stopped" distinctly, because it is only
		// reached after the caller is established as an authenticated member of the same
		// tenant who is allowed to see this workspace. The rule against confirming
		// existence (§7) is for callers who have not got that far.
		//
		// Do not start it (decision 16): that would wake someone else's container on
		// someone else's bill.
		http.Error(w, "that workspace is not running — its preview URL is issued at start",
			http.StatusConflict)
		return
	}
	q := url.Values{}
	q.Set("slug", ws.PreviewSlug)
	q.Set("port", strconv.Itoa(port))
	q.Set("next", safePreviewNext(r.URL.Query().Get("next")))
	http.Redirect(w, r, previewHandshakePath+"?"+q.Encode(), http.StatusFound)
}

// --- the relay itself -----------------------------------------------------------

type previewRelayOptions struct {
	port       int
	path       string // path as the browser asked for it (host mode: already at root)
	publicHost string // what to put in X-Forwarded-Host
	proto      string
	prefix     string // X-Forwarded-Prefix (path mode only; empty in host mode)
	public     bool   // public mode: add X-Robots-Tag
	// allowOrigin is the sibling preview origin the response may be read by, or ""
	// (the default: the CP adds no CORS at all and the app's own headers stand).
	allowOrigin string
	// identityHeader is manager.emailHeader — the deployment's own identity header,
	// which must never reach the previewed app. Passed in rather than read from a
	// global so a handler built in a test strips exactly what the real one does.
	identityHeader string
}

// previewProxyTransport is the CP→Agent transport (Service Connect / Cloud Map aware),
// shared by every preview relay.
var previewProxyTransport = newAgentTransport()

// relayPreview proxies one request to the workspace Agent's /proxy/{port}{path} with
// httputil.ReverseProxy — which is what makes WebSocket (Vite / Next HMR, Spring STOMP)
// and streaming responses work at all (ADR 0062 decision 10). A hand-rolled http.Client
// round trip can do neither.
func relayPreview(w http.ResponseWriter, r *http.Request, rt runtime.Runtime, o previewRelayOptions) {
	target, err := url.Parse(rt.Endpoint())
	if err != nil || target.Host == "" {
		http.Error(w, "workspace agent unreachable (is the workspace running?)", http.StatusBadGateway)
		return
	}
	path := o.path
	if path == "" {
		path = "/"
	}
	rp := &httputil.ReverseProxy{
		Transport: previewProxyTransport,
		// -1 = flush as soon as anything is written. Required for SSE and for the
		// App Router's streamed HTML: buffering it turns "slow" into "white page"
		// (docs/log/81 §2.5 (f)).
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = target.Scheme
			pr.Out.URL.Host = target.Host
			pr.Out.URL.Path = "/proxy/" + strconv.Itoa(o.port) + path
			pr.Out.URL.RawPath = ""
			pr.Out.Host = target.Host
			stripConsoleCredentials(pr.Out, r, o.identityHeader)
			if tok := rt.Token(); tok != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+tok) // CP↔Agent auth
			}
			// Rewrite mode strips X-Forwarded-* from Out, so the public name has to be
			// put back explicitly here (the Agent re-adds it for the same reason). This
			// is precisely the condition for Next.js Server Actions not to 403
			// (decision 9).
			pr.Out.Header.Set("X-Forwarded-Host", o.publicHost)
			pr.Out.Header.Set("X-Forwarded-Proto", o.proto)
			if o.prefix != "" {
				pr.Out.Header.Set("X-Forwarded-Prefix", o.prefix)
			} else {
				// In host mode the app sits at the root, so sending a prefix would
				// give it a wrong one.
				pr.Out.Header.Del("X-Forwarded-Prefix")
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			stripAppLoginCookies(resp)
			if o.allowOrigin != "" {
				// Drop whatever the app set before overwriting: with two of them
				// present the browser treats the header as invalid and ignores both,
				// which fails as "the app allows it too and it still does not work" —
				// the least legible breakage of the lot.
				resp.Header.Del("Access-Control-Allow-Origin")
				resp.Header.Del("Access-Control-Allow-Credentials")
				writePreviewCORS(resp.Header, o.allowOrigin)
			}
			if o.public {
				// A publicly served screen turning up in search results is an
				// accident, so always set it.
				resp.Header.Set("X-Robots-Tag", "noindex, nofollow")
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("preview relay (port=%d): %v", o.port, err)
			http.Error(w, "nothing is listening on 127.0.0.1:"+strconv.Itoa(o.port)+
				" inside the workspace (or the workspace is not running)", http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}

// stripConsoleCredentials removes everything that would let the previewed app act as
// the signed-in person. On a preview HOST the browser does not even send the Console's
// cookies (different origin), but a front auth gateway's headers and the path-mode
// caller's cookies still arrive here, and this is the one place both are cleaned.
func stripConsoleCredentials(out *http.Request, in *http.Request, identityHeader string) {
	out.Header.Del("Authorization")
	for _, h := range []string{
		"X-Forwarded-Email", "X-Forwarded-User", "X-Forwarded-Preferred-Username",
		"X-Auth-Request-Email", "X-Auth-Request-User", "X-Auth-Request-Access-Token",
	} {
		out.Header.Del(h)
	}
	if identityHeader != "" {
		out.Header.Del(identityHeader)
	}
	out.Header.Del("Cookie")
	var kept []string
	for _, c := range in.Cookies() {
		if sensitiveBrowserCookie(c.Name) {
			continue
		}
		kept = append(kept, c.Name+"="+c.Value)
	}
	if len(kept) > 0 {
		out.Header.Set("Cookie", strings.Join(kept, "; "))
	}
}

// stripAppLoginCookies drops Set-Cookie lines that would (re)issue a login on the
// Console or on a front auth gateway. The app's own cookies pass.
func stripAppLoginCookies(resp *http.Response) {
	vals := resp.Header.Values("Set-Cookie")
	if len(vals) == 0 {
		return
	}
	kept := vals[:0:0]
	for _, v := range vals {
		if sensitiveBrowserCookie(setCookieName(v)) {
			continue
		}
		kept = append(kept, v)
	}
	resp.Header.Del("Set-Cookie")
	for _, v := range kept {
		resp.Header.Add("Set-Cookie", v)
	}
}
