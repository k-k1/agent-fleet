// Package writegate refuses a state-changing request that a page other than the Console could
// have made with the member's login cookie, and adds the Console's framing headers.
//
// The login cookie is SameSite=Lax, which treats a sibling subdomain (a preview host) as same-site,
// so the cookie is still sent and a form or no-cors fetch needs no preflight. Two independent
// checks, neither a fallback for the other:
//
//   - Sec-Fetch-Site, when the browser sends it, must be same-origin (or none);
//   - Origin, when sent, must be the Console's own host (the caller's SameOrigin, the rule the
//     member-origin mark and the WebSocket upgrade already use).
//
// A caller that sends neither header is not a browser. Behind AUTH=proxy the gateway has to keep
// Host (or PUBLIC_BASE_URL has to name the public host), as the WebSocket upgrade already requires.
//
// Body format: under /api/ a request that carries a body must be application/json, except
// the explicitly listed upload routes, which take multipart/form-data. Body-less POST/DELETE carry no Content-Type and
// pass. Handlers decode JSON without looking at Content-Type, so this is what stops a text/plain
// no-preflight body from being parsed as JSON. Routes outside /api/ (the dev-IdP and MCP consent
// forms, the user's own preview app) keep their own body formats.
package writegate

import (
	"mime"
	"net/http"
	"strings"
)

// Rejection is why a request was refused.
type Rejection struct {
	Status  int
	Code    string
	Message string
}

// Policy carries what only the Control Plane knows.
type Policy struct {
	// Exempt reports a path that is not cookie-authenticated (the authGate exemption registry:
	// /internal/, /git/, /mcp, /engine/, the OAuth callbacks). Never hardcode a list here.
	Exempt func(path string) bool
	// SameOrigin reports whether the request's Origin header names the Console's own host.
	SameOrigin func(r *http.Request) bool
	// Multipart reports a method+path that really reads multipart/form-data (an upload). Every
	// other /api/ route that carries a body takes application/json only.
	Multipart func(r *http.Request) bool
}

// Check returns nil when the request may proceed.
func (p Policy) Check(r *http.Request) *Rejection {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return nil
	}
	if p.Exempt(r.URL.EscapedPath()) {
		return nil
	}
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" && s != "none" {
		return &Rejection{http.StatusForbidden, "forbidden", "writes are accepted from the Console's own origin only"}
	}
	if r.Header.Get("Origin") != "" && !p.SameOrigin(r) {
		return &Rejection{http.StatusForbidden, "forbidden", "writes are accepted from the Console's own origin only"}
	}
	// An unknown length (chunked) counts as a body.
	if strings.HasPrefix(r.URL.Path, "/api/") && r.ContentLength != 0 {
		mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || !(mt == "application/json" || (mt == "multipart/form-data" && p.Multipart != nil && p.Multipart(r))) {
			return &Rejection{http.StatusUnsupportedMediaType, "unsupported_media_type", "writes take application/json"}
		}
	}
	return nil
}

// FrameHeaders adds the framing and plugin protections to every Console response: a same-site
// page must not be able to frame the Console, and the Console has no <object>/<embed> use.
// script-src is deliberately absent: index.html carries an inline bootstrap. The path-form
// preview serves the user's own app and keeps its own policy.
func FrameHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/preview/") {
			h := w.Header()
			h.Set("X-Frame-Options", "DENY")
			h.Set("Content-Security-Policy", "frame-ancestors 'self'; object-src 'none'; base-uri 'self'")
		}
		next.ServeHTTP(w, r)
	})
}
