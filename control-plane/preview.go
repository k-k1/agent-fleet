package main

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// handlePreview proxies /preview/{port}/{rest...} to the caller's Workspace Agent
// at /proxy/{port}/..., which in turn reaches a service the user started inside
// the container (Spring Boot, dev server, ...). Auth reuses rtFor — the same
// gateway-resolved identity as every other route — and the CP↔Agent bearer is
// injected here. We attach X-Forwarded-* so apps that honor them (Spring Boot's
// server.forward-headers-strategy) generate correct absolute URLs/redirects under
// the /preview/{port} sub-path. HTTP only for now; WebSocket/HMR is a follow-up.
func (c config) handlePreview(w http.ResponseWriter, r *http.Request) {
	res, ok := c.resolvedFor(w, r)
	if !ok {
		return
	}
	rt := res.rt
	c.mgr.conns.touch(res.ws.ID) // P3-9: preview traffic keeps the workspace warm
	port := r.PathValue("port")
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		http.Error(w, "bad preview port", http.StatusBadRequest)
		return
	}

	rest := r.PathValue("rest")
	target := rt.agentBase() + "/proxy/" + port + "/" + rest
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, "bad proxy request", http.StatusBadGateway)
		return
	}
	req.Header = r.Header.Clone()
	if rt.token != "" {
		req.Header.Set("Authorization", "Bearer "+rt.token) // CP↔Agent auth
	}
	// Tell the previewed app where it really lives, as the browser sees it:
	// <public base path>/preview/{port}. Spring Boot uses these to build correct
	// links/redirects; a plain JSON REST app ignores them and still works.
	req.Header.Set("X-Forwarded-Prefix", c.externalPrefix()+"/preview/"+port)
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

// handlePreviewRedirect bounces /preview/{port} to /preview/{port}/ so the
// previewed app resolves its relative assets under the sub-path. The target is
// rebuilt with the external prefix because a path-stripping proxy (Caddy) has
// already removed it from r.URL.Path — redirecting to that bare path would drop
// /agent-fleet and escape the mount.
func (c config) handlePreviewRedirect(w http.ResponseWriter, r *http.Request) {
	target := c.externalPrefix() + "/preview/" + r.PathValue("port") + "/"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

// externalPrefix is the URL path the deployment is mounted under as seen by the
// browser (e.g. "/agent-fleet"), derived from PUBLIC_BASE_URL. Empty when CP is
// served at the root (local dev).
func (c config) externalPrefix() string {
	if c.publicBaseURL == "" {
		return ""
	}
	if u, err := url.Parse(c.publicBaseURL); err == nil {
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
