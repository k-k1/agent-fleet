package main

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// previewAPI は /preview/{port} プロキシの機能ハンドラ集（docs/23 残③）。解決は
// 埋め込みの memberAuth（登録側で withResolved に包む — 従来の resolvedFor と同一
// 判定）。X-Forwarded-Prefix の組み立てに使う publicBaseURL だけ config から写す。
type previewAPI struct {
	memberAuth
	publicBaseURL string
}

func newPreviewAPI(m *manager, publicBaseURL string) previewAPI {
	return previewAPI{memberAuth{m}, publicBaseURL}
}

// previewTenantCookie carries the tenant resolved for a /preview/{port} tab across
// the sub-requests the previewed page issues on its own — relative CSS/JS and its
// own API calls never carry the Console's ?tenant= query, so without this they'd
// resolve with no tenant and 401/409. Path-scoped per port (see
// withPreviewResolved), so it never crosses to another preview tab.
const previewTenantCookie = "af_pv_tenant"

// previewTenantSel resolves the tenant for one /preview/{port} request: an
// explicit X-AF-Tenant header or ?tenant= query (only the Console's own first
// navigation sends one) always wins and is reported back via explicit=true so the
// caller can (re)mint the cookie; otherwise fall back to the cookie minted by that
// first navigation. The browser only attaches the cookie for requests under the
// path it was scoped to, so no port/tenant lookup is needed here to keep tabs
// apart.
func previewTenantSel(r *http.Request) (sel string, explicit bool) {
	if s := tenantSel(r); s != "" {
		return s, true
	}
	if c, err := r.Cookie(previewTenantCookie); err == nil && c.Value != "" {
		return c.Value, false
	}
	return "", false
}

// withPreviewResolved is withResolved's /preview/{port} counterpart: same
// identity/tenant/membership resolution (so a tampered or stale cookie still
// fails safe — selectMembership re-checks membership on every request, same as
// header/query — 401/403/409 unchanged), but the tenant can also come from
// previewTenantSel's cookie fallback. When the request carried an explicit
// selection, the cookie is (re)minted scoped to this exact port's path so the
// app's own follow-up requests (style.css, app.js, api/...) inherit it.
func (a previewAPI) withPreviewResolved(h func(http.ResponseWriter, *http.Request, *resolved)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := a.mgr.resolveIdentity(r)
		if id.key == "" {
			writeAPIErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "no gateway identity"})
			return
		}
		sel, explicit := previewTenantSel(r)
		res, aerr := a.mgr.resolveFull(r.Context(), id.key, id.email, sel)
		if aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		if explicit {
			a.setPreviewTenantCookie(w, r, sel)
		}
		h(w, r, res)
	}
}

// setPreviewTenantCookie scopes the cookie's Path to this exact port under the
// deployment's external prefix, e.g. "/agent-fleet/preview/8080/" — so it rides
// along with every sub-request this preview tab issues but never with another
// port's (a second preview tab may be a different tenant). No Max-Age: it lives
// only for the browser session, matching how long the plain query-string
// selection would have worked before this change. Plain (unsigned) value: a
// tampered cookie can only name a tenant the caller isn't a member of, which
// selectMembership already rejects on every request, so it carries no more trust
// than the ?tenant= query it stands in for.
func (a previewAPI) setPreviewTenantCookie(w http.ResponseWriter, r *http.Request, tenant string) {
	http.SetCookie(w, &http.Cookie{
		Name:     previewTenantCookie,
		Value:    tenant,
		Path:     a.externalPrefix() + "/preview/" + r.PathValue("port") + "/",
		HttpOnly: true,
		Secure:   forwardedProto(r) == "https",
		SameSite: http.SameSiteLaxMode,
	})
}

// proxy proxies /preview/{port}/{rest...} to the caller's Workspace Agent
// at /proxy/{port}/..., which in turn reaches a service the user started inside
// the container (Spring Boot, dev server, ...). Auth reuses withResolved — the same
// gateway-resolved identity as every other route — and the CP↔Agent bearer is
// injected here. We attach X-Forwarded-* so apps that honor them (Spring Boot's
// server.forward-headers-strategy) generate correct absolute URLs/redirects under
// the /preview/{port} sub-path. HTTP only for now; WebSocket/HMR is a follow-up.
func (a previewAPI) proxy(w http.ResponseWriter, r *http.Request, res *resolved) {
	rt := res.rt
	if err := a.mgr.touchWorkspace(r.Context(), res.ws.ID); err != nil {
		writeAPIErr(w, workspaceActivityAPIError(err))
		return
	}
	port := r.PathValue("port")
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		http.Error(w, "bad preview port", http.StatusBadRequest)
		return
	}

	rest := r.PathValue("rest")
	if unsafeRelayPath(rest) {
		http.Error(w, "bad preview path", http.StatusBadRequest)
		return
	}
	target := rt.Endpoint() + "/proxy/" + port + "/" + rest
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, "bad proxy request", http.StatusBadGateway)
		return
	}
	req.Header = a.sanitizedHeader(r)
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token()) // CP↔Agent auth
	}
	// Tell the previewed app where it really lives, as the browser sees it:
	// <public base path>/preview/{port}. Spring Boot uses these to build correct
	// links/redirects; a plain JSON REST app ignores them and still works.
	req.Header.Set("X-Forwarded-Prefix", a.externalPrefix()+"/preview/"+port)
	if r.Host != "" && req.Header.Get("X-Forwarded-Host") == "" {
		req.Header.Set("X-Forwarded-Host", r.Host)
	}
	if req.Header.Get("X-Forwarded-Proto") == "" {
		req.Header.Set("X-Forwarded-Proto", forwardedProto(r))
	}

	resp, err := agentRelayClient.Do(req)
	if err != nil {
		http.Error(w, "workspace agent unreachable (is the workspace running?)", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vals := range resp.Header {
		for _, v := range vals {
			// The previewed app must not (re)issue a login cookie on the Console
			// origin — neither the CP's own (af_session fixation / tossing) nor a
			// front auth gateway's (_oauth2_proxy* / AWSELBAuthSessionCookie*):
			// the same set the request side strips. Its other cookies pass.
			if http.CanonicalHeaderKey(k) == "Set-Cookie" && sensitiveBrowserCookie(setCookieName(v)) {
				continue
			}
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// sanitizedHeader clones the browser's request headers for the relay WITHOUT the
// Console's credentials. The previewed app is arbitrary user code (someone's dev
// server): forwarding the af_session cookie, the gateway identity header, or a
// browser Authorization header would let that app call the CP API as the signed-in
// user. CP-owned cookies are filtered out of Cookie (the previewed app's own
// cookies still pass); the identity headers cover both the configured emailHeader
// and the common oauth2-proxy set an upstream may attach.
func (a previewAPI) sanitizedHeader(r *http.Request) http.Header {
	h := r.Header.Clone()
	h.Del("Authorization")
	h.Del("X-Forwarded-Email")
	h.Del("X-Forwarded-User")
	h.Del("X-Forwarded-Preferred-Username")
	h.Del("X-Auth-Request-Email")
	h.Del("X-Auth-Request-User")
	h.Del("X-Auth-Request-Access-Token")
	if eh := a.mgr.emailHeader; eh != "" {
		h.Del(eh)
	}
	h.Del("Cookie")
	var kept []string
	for _, c := range r.Cookies() {
		if sensitiveBrowserCookie(c.Name) {
			continue
		}
		kept = append(kept, c.Name+"="+c.Value)
	}
	if len(kept) > 0 {
		h.Set("Cookie", strings.Join(kept, "; "))
	}
	return h
}

// sensitiveBrowserCookie reports whether a cookie carries a login the previewed
// app must not see: the CP's own cookies, plus the session cookies a FRONT auth
// gateway sets on the same host (oauth2-proxy `_oauth2_proxy*` incl. chunked
// variants, ALB OIDC `AWSELBAuthSessionCookie-*`) — replaying either lets the
// app act as the signed-in user against the gateway-protected origin.
// af_pv_tenant is AF's own preview-tenant resolution (this file's cookie), not a
// login, but it must not leak to the app either — the app must stay ignorant of
// AF's tenant-selection mechanism, same as it never sees ?tenant= itself.
func sensitiveBrowserCookie(name string) bool {
	if name == sessionCookie || name == stateCookie || name == previewTenantCookie {
		return true
	}
	return strings.HasPrefix(name, "_oauth2_proxy") ||
		strings.HasPrefix(name, "AWSELBAuthSessionCookie")
}

// setCookieName extracts the cookie name from a Set-Cookie header line.
func setCookieName(setCookie string) string {
	name, _, _ := strings.Cut(setCookie, "=")
	return strings.TrimSpace(name)
}

// redirect bounces /preview/{port} to /preview/{port}/ so the
// previewed app resolves its relative assets under the sub-path. The target is
// rebuilt with the external prefix because a path-stripping proxy (Caddy) has
// already removed it from r.URL.Path — redirecting to that bare path would drop
// /agent-fleet and escape the mount. No resolution preamble, so it registers unwrapped.
func (a previewAPI) redirect(w http.ResponseWriter, r *http.Request) {
	target := a.externalPrefix() + "/preview/" + r.PathValue("port") + "/"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

// externalPrefix is the URL path the deployment is mounted under as seen by the
// browser (e.g. "/agent-fleet"), derived from PUBLIC_BASE_URL. Empty when CP is
// served at the root (local dev).
func (a previewAPI) externalPrefix() string {
	if a.publicBaseURL == "" {
		return ""
	}
	if u, err := url.Parse(a.publicBaseURL); err == nil {
		return strings.TrimRight(u.Path, "/")
	}
	return ""
}

// forwardedProto reports the original scheme, trusting an upstream
// X-Forwarded-Proto (Caddy/oauth2-proxy set it) before falling back to the CP's
// own connection.
func forwardedProto(r *http.Request) string {
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		return p
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}
