package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// opencode web (/ocweb) — a dedicated proxy for the prefix-aware opencode web UI
// (pk-opencode-webui), distinct from the generic /preview/{port}. Unlike preview
// it (1) preserves the full path (the UI's BASE_PATH expects /ocweb/… verbatim,
// not a stripped root) and (2) tunnels WebSocket + SSE (terminal PTY / event
// stream). Auth reuses rtFor; the CP↔Agent bearer is injected.
// See docs/decisions/0007-opencode-web-via-pk-webui.md.

// handleOcweb reverse-proxies {extPrefix}/ocweb/… to the caller's Agent /ocweb/…,
// which fronts the per-workspace pk-webui. httputil.ReverseProxy carries both
// plain HTTP and WebSocket upgrades.
func (c config) handleOcweb(w http.ResponseWriter, r *http.Request) {
	res, ok := c.resolvedFor(w, r)
	if !ok {
		return
	}
	rt := res.rt
	c.mgr.conns.touch(res.ws.ID) // P3-9: opencode web traffic keeps the workspace warm
	agentURL, err := url.Parse(rt.agentBase())
	if err != nil {
		http.Error(w, "bad agent base", http.StatusBadGateway)
		return
	}
	rp := httputil.NewSingleHostReverseProxy(agentURL)
	director := rp.Director
	rp.Director = func(req *http.Request) {
		director(req) // scheme/host → agent; req.URL.Path kept as /ocweb/…
		if rt.token != "" {
			req.Header.Set("Authorization", "Bearer "+rt.token) // CP↔Agent auth
		}
		// Tell pk-webui where it really lives as the browser sees it.
		req.Header.Set("X-Forwarded-Prefix", c.externalPrefix()+"/ocweb")
		if req.Header.Get("X-Forwarded-Proto") == "" {
			req.Header.Set("X-Forwarded-Proto", forwardedProto(r))
		}
		if req.Header.Get("X-Forwarded-Host") == "" && r.Host != "" {
			req.Header.Set("X-Forwarded-Host", r.Host)
		}
	}
	rp.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "workspace agent unreachable (is the workspace running?)", http.StatusBadGateway)
	}
	rp.ServeHTTP(w, r)
}

// handleOcwebRedirect bounces /ocweb → /ocweb/ so the UI resolves its assets under
// the sub-path (rebuilt with the external prefix, like handlePreviewRedirect).
func (c config) handleOcwebRedirect(w http.ResponseWriter, r *http.Request) {
	target := c.externalPrefix() + "/ocweb/"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

// handleOpencodeWebToggle proxies PUT /api/agents/opencode-web to the Agent, but
// injects base_prefix = externalPrefix so the Agent starts pk-webui with a
// BASE_PATH matching what the browser sees. The Agent cannot know the deployment's
// external prefix on its own. GET /api/agents/opencode-web uses proxyAgentREST.
func (c config) handleOpencodeWebToggle(w http.ResponseWriter, r *http.Request) {
	rt, ok := c.rtFor(w, r)
	if !ok {
		return
	}
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body == nil {
		body = map[string]any{}
	}
	body["base_prefix"] = c.externalPrefix()
	buf, _ := json.Marshal(body)

	target := rt.agentBase() + strings.TrimPrefix(r.URL.Path, "/api")
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(buf))
	if err != nil {
		http.Error(w, "bad proxy request", http.StatusBadGateway)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if rt.token != "" {
		req.Header.Set("Authorization", "Bearer "+rt.token) // CP↔Agent auth
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
