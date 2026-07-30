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

// proxy proxies /preview/{port}/{rest...} to the caller's Workspace Agent
// at /proxy/{port}/..., which in turn reaches a service the user started inside
// the container (Spring Boot, dev server, ...). Auth reuses withResolved — the same
// gateway-resolved identity as every other route — and the CP↔Agent bearer is
// injected here. We attach X-Forwarded-* so apps that honor them (Spring Boot's
// server.forward-headers-strategy) generate correct absolute URLs/redirects under
// the /preview/{port} sub-path. HTTP only for now; WebSocket/HMR is a follow-up.
func (a previewAPI) proxy(w http.ResponseWriter, r *http.Request, res *resolved) {
	rt := res.rt
	a.mgr.conns.touch(res.ws.ID) // P3-9: preview traffic keeps the workspace warm
	port := r.PathValue("port")
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		http.Error(w, "bad preview port", http.StatusBadRequest)
		return
	}

	rest := r.PathValue("rest")
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

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "workspace agent unreachable (is the workspace running?)", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vals := range resp.Header {
		for _, v := range vals {
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
		if c.Name == sessionCookie || c.Name == stateCookie {
			continue
		}
		kept = append(kept, c.Name+"="+c.Value)
	}
	if len(kept) > 0 {
		h.Set("Cookie", strings.Join(kept, "; "))
	}
	return h
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
