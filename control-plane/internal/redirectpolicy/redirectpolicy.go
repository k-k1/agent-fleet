// Package redirectpolicy is the CheckRedirect for every Control Plane HTTP client that sends a
// credential (an Authorization header, or a secret in the body). Go's default policy re-sends
// Authorization to another port of the same host, to subdomains and across an HTTPS to HTTP
// hop, and on 307/308 re-sends the body, so a 3xx from the endpoint could carry the credential
// off its origin. workspace/agent/internal/redirectpolicy holds the same policy for the other
// Go module; the two cannot share a package, so keep them in step.
package redirectpolicy

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrRefused marks a redirect the policy refuses (another origin, a downgrade, a write, a
// loop). It is deterministic, so a retry loop must treat it as final: re-sending only repeats
// the first request.
var ErrRefused = errors.New("redirect refused")

// maxHops is the number of requests in a chain at which the policy stops (net/http's own limit).
const maxHops = 10

// Origin is scheme://host:port with the scheme's default port made explicit, so
// "https://h" and "https://h:443" are one origin and nothing looser is.
func Origin(u *url.URL) string {
	scheme, port := strings.ToLower(u.Scheme), u.Port()
	if port == "" {
		switch scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	host := u.Hostname()
	// Only ASCII letters are folded, and an IPv6 zone (case-sensitive, it names an interface) not
	// at all. strings.ToLower would also map U+0130 to "i", merging two hosts the resolver tells
	// apart; a Unicode and a punycode spelling of one host are refused instead (fail closed).
	zone := ""
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host, zone = host[:i], host[i:]
	}
	host = strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, host) + zone
	return scheme + "://" + net.JoinHostPort(host, port)
}

// SameOriginReadOnly follows a redirect only for a GET or HEAD, only within the exact origin of
// the first request, and never from HTTPS to HTTP. A write is never redirected: Go turns a
// 301/302/303 POST/PUT/DELETE into a GET, whose 200 would read as the write having succeeded,
// and 307/308 re-send the body. Anything else fails the call with ErrRefused instead of sending
// the credential there.
func SameOriginReadOnly(req *http.Request, via []*http.Request) error {
	first := via[0].URL
	if via[0].Method != http.MethodGet && via[0].Method != http.MethodHead {
		return ErrRefused
	}
	if len(via) >= maxHops {
		return ErrRefused
	}
	if first.Scheme == "https" && req.URL.Scheme != "https" {
		return ErrRefused
	}
	if Origin(req.URL) != Origin(first) {
		return ErrRefused
	}
	return nil
}

// IsRefused reports whether err is (or wraps) a refusal by SameOriginReadOnly.
func IsRefused(err error) bool { return errors.Is(err, ErrRefused) }

// Client is an http.Client with SameOriginReadOnly and the given timeout (0 = none, for a caller
// that bounds each call with a context). Use it instead of http.DefaultClient or a bare
// &http.Client{} for any request that carries a credential.
func Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: SameOriginReadOnly}
}
