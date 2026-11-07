// Package relayhdr is the allow-list of request headers a relayed request keeps when it
// goes on to the workspace Agent or to a preview app.
package relayhdr

import (
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
)

// Everything else is dropped, because a front gateway (oauth2-proxy, an
// ALB OIDC listener, a corporate proxy) can add access-token, group and identity headers
// under names no denylist anticipates, and neither the Agent nor a previewed app should
// receive them. Authorization, the member-origin mark, the deployment's identity header
// and cookies are decided by the caller (stripConsoleCredentials), not here.
//
// The list is what the Agent's routes and the apps previewed so far read: content
// negotiation, conditional and range requests, the WebSocket handshake (Sec-*), the
// forwarded host / proto / prefix the Agent and Next.js / Spring depend on, and the few
// framework request headers an app cannot work without. An app that needs another header
// is served by AF_RELAY_EXTRA_HEADERS rather than by widening this list.
var allowedHeaders = map[string]bool{
	"Accept": true, "Accept-Encoding": true, "Accept-Language": true,
	"Cache-Control": true, "Pragma": true,
	"Content-Type": true, "Content-Length": true, "Content-Encoding": true,
	"Content-Disposition": true, "Content-Range": true,
	"Expect": true, "Range": true, "If-Range": true, "If-Match": true,
	"If-None-Match": true, "If-Modified-Since": true, "If-Unmodified-Since": true,
	"Last-Event-Id": true,
	"Origin":        true, "Referer": true, "User-Agent": true, "Dnt": true, "Priority": true,
	"Upgrade-Insecure-Requests": true,
	// Connection management; net/http and ReverseProxy own the hop-by-hop ones, and the
	// WebSocket upgrade needs these two to survive on the clone relays.
	"Connection": true, "Upgrade": true, "Te": true,
	"X-Forwarded-Host": true, "X-Forwarded-Proto": true, "X-Forwarded-Prefix": true,
	"X-Forwarded-Port": true,
	"X-Requested-With": true, "X-Csrf-Token": true, "X-Csrftoken": true, "X-Xsrf-Token": true,
	"Rsc": true, "Traceparent": true, "Tracestate": true, "Baggage": true,
	"X-Request-Id": true,
}

// allowedPrefixes are families: Sec-WebSocket-* (handshake), Sec-Fetch-* (Fetch Metadata) and
// Sec-CH-* (Client Hints). Other Sec-* headers are not metadata: Sec-Token-Binding,
// Sec-Private-State-Token, Sec-Redemption-Record and Sec-Secure-Session-Id carry
// credential or session material meant for the Console or the gateway, so they stay out. Also the preflight's Access-Control-Request-*, and the request headers of Next.js, htmx and
// Turbo (Next-Action is what a Server Action is recognised by).
var allowedPrefixes = []string{
	"Sec-Websocket-", "Sec-Fetch-", "Sec-Ch-", "Access-Control-Request-", "Next-", "Hx-", "Turbo-",
}

// extraHeaders is AF_RELAY_EXTRA_HEADERS: comma-separated header names an operator
// adds to the allow-list; a trailing "*" makes an entry a prefix.
var extraHeaders = ParseExtra(os.Getenv("AF_RELAY_EXTRA_HEADERS"))

// ParseExtra parses an AF_RELAY_EXTRA_HEADERS value.
func ParseExtra(s string) []string {
	var out []string
	for _, n := range strings.Split(s, ",") {
		if n = strings.TrimSpace(n); n != "" {
			if strings.HasSuffix(n, "*") {
				out = append(out, http.CanonicalHeaderKey(strings.TrimSuffix(n, "*"))+"*")
			} else {
				out = append(out, http.CanonicalHeaderKey(n))
			}
		}
	}
	return out
}

// Allowed reports whether a canonical header name is on the allow-list.
func Allowed(canonical string) bool {
	if allowedHeaders[canonical] {
		return true
	}
	for _, p := range allowedPrefixes {
		if strings.HasPrefix(canonical, p) {
			return true
		}
	}
	for _, e := range extraHeaders {
		if p, ok := strings.CutSuffix(e, "*"); ok {
			if strings.HasPrefix(canonical, p) {
				return true
			}
		} else if e == canonical {
			return true
		}
	}
	return false
}

// droppedLogged remembers which dropped names were already logged: one line per
// name per process is enough to find a compatibility problem without a line per request.
// The cap stops a client that invents header names from growing it.
var (
	droppedMu     sync.Mutex
	droppedLogged = map[string]bool{}
)

const droppedLogCap = 200

// noteDropped logs the NAME (never the value) of a dropped header, once per process.
func noteDropped(name string) {
	droppedMu.Lock()
	defer droppedMu.Unlock()
	if droppedLogged[name] || len(droppedLogged) >= droppedLogCap {
		return
	}
	droppedLogged[name] = true
	log.Printf("relay: dropped request header %q (not on the relay allow-list; AF_RELAY_EXTRA_HEADERS adds it)", name)
}

// KeepAllowed deletes every header of h that is not on the allow-list.
// Cookie is handled by the caller.
func KeepAllowed(h http.Header) {
	for k := range h {
		if k == "Cookie" || Allowed(k) {
			continue
		}
		h.Del(k)
		noteDropped(k)
	}
}

// SetExtra replaces the operator's extra names and returns a function that restores them;
// it exists for tests.
func SetExtra(v []string) (restore func()) {
	old := extraHeaders
	extraHeaders = v
	return func() { extraHeaders = old }
}
