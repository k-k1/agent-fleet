// workspace_listener.go — the CP's second listener, for workspaces only (ADR 0106 decision 8).
//
// The ordinary listener is safe only because the ingress is its sole client
// (docs/build/09-deploy.md §9.3): AUTH=proxy trusts the identity header without stripping
// it, and withClientIP reads X-Forwarded-For by hop count without asking who sent it. A
// workspace connecting to that port directly could therefore name any user and any client
// address. Where workspaces cannot reach the CP through the ingress (Kubernetes), they get
// this listener instead: the routes the Agent calls and nothing else, each authenticated by
// its own per-membership bearer token, with every identity and forwarding header removed
// and the client taken from the connection itself.
package main

import (
	"context"
	"log"
	"net/http"
	"time"
)

// workspaceRoutes is every pattern the workspace listener serves, spelled exactly as it is
// registered in buildMux. It is a list of patterns and not of path prefixes on purpose:
// /internal/ also carries the egress proxy's routes (/internal/egress*), which a workspace
// must never reach. Every entry authenticates with a bearer token of its own and reads
// neither the session cookie, the identity header nor the client address.
//
// Adding an Agent → CP call means adding its pattern here and its request to
// TestWorkspaceListenerServesEveryAgentCall: a call left off works everywhere except on a
// deployment that sets AF_CP_INTERNAL_LISTEN.
var workspaceRoutes = map[string]bool{
	// Docs pull (AF_DOCS_TOKEN).
	"GET /internal/docs": true,
	// Branch naming rules (AF_BRANCH_RULES_TOKEN).
	"GET /internal/branch-rules": true,
	// Tenant MCP registry (AF_MCP_TOKEN).
	"GET /internal/mcp-servers": true,
	// AWS profiles (AF_AWS_PROFILES_TOKEN).
	"GET /internal/aws-profiles": true,
	// Memo queue (AF_MEMO_TOKEN). The /internal/memo-categories face is not listed: no
	// Agent code calls it.
	"GET /internal/memos":         true,
	"POST /internal/memos":        true,
	"POST /internal/memos/flush":  true,
	"PATCH /internal/memos/{id}":  true,
	"DELETE /internal/memos/{id}": true,
	// Schedules (AF_SCHEDULE_TOKEN).
	"GET /internal/schedules":               true,
	"POST /internal/schedules":              true,
	"PATCH /internal/schedules/{id}":        true,
	"DELETE /internal/schedules/{id}":       true,
	"POST /internal/schedules/{id}/pause":   true,
	"POST /internal/schedules/{id}/resume":  true,
	"POST /internal/schedules/{id}/run-now": true,
	"GET /internal/schedules/{id}/runs":     true,
	// Git OAuth refresh (AF_GIT_OAUTH_TOKEN).
	"POST /internal/git-oauth/bitbucket/refresh": true,
	"POST /internal/git-oauth/jira/refresh":      true,
	// Self-hosted engines (AF_ENGINE_ISSUE_TOKEN, then the session token). Registered only
	// when the deployment runs an engine; absent, they 404 here as they do on the main port.
	"POST /internal/engine/token":  true,
	"GET /internal/engine/catalog": true,
	"GET /engine/{key}/props":      true,
	"/engine/{key}/v1/{path...}":   true,
	// Internal git: smart HTTP and LFS (the Basic git token).
	"POST /git/{slug}/{repo}/info/lfs/objects/batch":     true,
	"PUT /git/{slug}/{repo}/info/lfs/objects/{oid}":      true,
	"GET /git/{slug}/{repo}/info/lfs/objects/{oid}":      true,
	"POST /git/{slug}/{repo}/info/lfs/locks":             true,
	"GET /git/{slug}/{repo}/info/lfs/locks":              true,
	"POST /git/{slug}/{repo}/info/lfs/locks/verify":      true,
	"POST /git/{slug}/{repo}/info/lfs/locks/{id}/unlock": true,
	"/git/{slug}/{repo...}":                              true,
}

// workspaceOnly serves a request through the same mux as the main listener, but only when
// the pattern that mux would pick is in workspaceRoutes; anything else is a 404, as if the
// route did not exist. Dispatching through the one mux, rather than registering the
// handlers a second time, keeps a single engine registry and a single set of handlers, so
// the two listeners cannot drift apart in what a route does — only in whether it is there.
func workspaceOnly(full *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Handler reports the pattern ServeHTTP would dispatch to, after the same path
		// cleaning; a redirect, a 405 and a miss all come back as a pattern not listed.
		if _, pattern := full.Handler(r); !workspaceRoutes[pattern] {
			http.NotFound(w, r)
			return
		}
		full.ServeHTTP(w, r)
	})
}

// workspaceUntrustedHeaders are the request headers a workspace could use to claim to be
// somebody or somewhere else. None of the workspace routes read them; removing them here
// keeps it that way for a handler written later.
var workspaceUntrustedHeaders = []string{
	"X-Forwarded-Email", "X-Forwarded-User", "X-Forwarded-Preferred-Username",
	"X-Auth-Request-Email", "X-Auth-Request-User", "X-Auth-Request-Access-Token",
	"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-IP", "Forwarded",
}

// workspaceEdge is the workspace listener's counterpart of withClientIP and the auth
// gate's header scrub: it drops the identity header (AUTH_EMAIL_HEADER, whatever it is
// named) and every forwarding header, and records the connection's own address as the
// client regardless of AF_TRUSTED_PROXY_HOPS — no proxy sits in front of this port.
func workspaceEdge(next http.Handler, identityHeader string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, h := range workspaceUntrustedHeaders {
			r.Header.Del(h)
		}
		if identityHeader != "" {
			r.Header.Del(identityHeader)
		}
		info := resolveClientIP(r, 0)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey{}, info)))
	})
}

// workspaceListenerHandler is the whole handler of the workspace listener: no auth gate
// (every route carries its own token), no preview dispatcher, no Console.
func workspaceListenerHandler(full *http.ServeMux, identityHeader string) http.Handler {
	return workspaceEdge(logRequests(gzipMiddleware(etagJSON(workspaceOnly(full)))), identityHeader)
}

// serveWorkspaceListener starts the workspace listener on addr. A listener that cannot
// bind is fatal, like the main one: workspaces configured to call it would otherwise fail
// one feature at a time with nothing in the log saying why.
func serveWorkspaceListener(addr string, full *http.ServeMux, identityHeader string) {
	srv := &http.Server{Addr: addr, Handler: workspaceListenerHandler(full, identityHeader), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("workspace listener on %s (%d routes, ADR 0106 decision 8)", addr, len(workspaceRoutes))
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			log.Fatalf("workspace listener (AF_CP_INTERNAL_LISTEN=%s): %v", addr, err)
		}
	}()
}
