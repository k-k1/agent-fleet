package main

import (
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
)

// The request headers a relayed request keeps when it goes on to the workspace Agent or
// to a preview app. Everything else is dropped, because a front gateway (oauth2-proxy, an
// ALB OIDC listener, a corporate proxy) can add access-token, group and identity headers
// under names no denylist anticipates, and neither the Agent nor a previewed app should
// receive them. Authorization, the member-origin mark, the deployment's identity header
// and cookies are decided in stripConsoleCredentials, not here.
//
// The list is what the Agent's routes and the apps previewed so far read: content
// negotiation, conditional and range requests, the WebSocket handshake (Sec-*), the
// forwarded host / proto / prefix the Agent and Next.js / Spring depend on, and the few
// framework request headers an app cannot work without. An app that needs another header
// is served by AF_RELAY_EXTRA_HEADERS rather than by widening this list.
var relayAllowedHeaders = map[string]bool{
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
	"X-Requested-With": true, "X-Csrf-Token": true, "X-Xsrf-Token": true,
	"Rsc": true, "Traceparent": true, "Tracestate": true, "Baggage": true,
	"X-Request-Id": true,
}

// relayAllowedHeaderPrefixes are families: Sec-WebSocket-*, Sec-Fetch-* and Sec-CH-*,
// the preflight's Access-Control-Request-*, and the request headers of Next.js, htmx and
// Turbo (Next-Action is what a Server Action is recognised by).
var relayAllowedHeaderPrefixes = []string{
	"Sec-", "Access-Control-Request-", "Next-", "Hx-", "Turbo-",
}

// relayExtraHeaders is AF_RELAY_EXTRA_HEADERS: comma-separated header names an operator
// adds to the allow-list; a trailing "*" makes an entry a prefix.
var relayExtraHeaders = parseRelayExtraHeaders(os.Getenv("AF_RELAY_EXTRA_HEADERS"))

func parseRelayExtraHeaders(s string) []string {
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

func relayHeaderAllowed(canonical string) bool {
	if relayAllowedHeaders[canonical] {
		return true
	}
	for _, p := range relayAllowedHeaderPrefixes {
		if strings.HasPrefix(canonical, p) {
			return true
		}
	}
	for _, e := range relayExtraHeaders {
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

// relayDroppedLogged remembers which dropped names were already logged: one line per
// name per process is enough to find a compatibility problem without a line per request.
// The cap stops a client that invents header names from growing it.
var (
	relayDroppedMu     sync.Mutex
	relayDroppedLogged = map[string]bool{}
)

const relayDroppedLogCap = 200

// relayNoteDropped logs the NAME (never the value) of a dropped header, once per process.
func relayNoteDropped(name string) {
	relayDroppedMu.Lock()
	defer relayDroppedMu.Unlock()
	if relayDroppedLogged[name] || len(relayDroppedLogged) >= relayDroppedLogCap {
		return
	}
	relayDroppedLogged[name] = true
	log.Printf("relay: dropped request header %q (not on the relay allow-list; AF_RELAY_EXTRA_HEADERS adds it)", name)
}

// keepAllowedRelayHeaders deletes every header of h that is not on the allow-list.
// Cookie is handled by the caller.
func keepAllowedRelayHeaders(h http.Header) {
	for k := range h {
		if k == "Cookie" || relayHeaderAllowed(k) {
			continue
		}
		h.Del(k)
		relayNoteDropped(k)
	}
}
