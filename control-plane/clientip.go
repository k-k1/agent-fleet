// clientip.go — who is on the other end of this request, as an IP (docs/log/66, ADR 0047).
//
// Until this file the CP never read r.RemoteAddr at all, which is the honest starting
// point: behind an ALB it is the load balancer, and the real client is in
// X-Forwarded-For — a header ANY client can set. Getting that wrong in the usual way
// (read the leftmost entry) hands every caller the ability to name their own source
// address, which would make a tenant's network restriction worse than not having one.
//
// The rule here is the only safe one: proxies APPEND the peer they received from, so
// with N trusted proxies in front of the CP the client is XFF[len-N] — counted from
// the RIGHT. A forged prefix is simply ignored, because the trusted hops added their
// entries after it. N is a property of the DEPLOYMENT (AF_TRUSTED_PROXY_HOPS), not
// something the product can infer: on docker/native with no proxy it is 0 and
// RemoteAddr is the truth; behind the ALB it is 1; behind a CDN + ALB it is 2.
package main

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// trustedProxyHops is how many proxies sit between the client and this process.
// Read once at boot so a request cannot change it.
var trustedProxyHops = runtime.EnvInt("AF_TRUSTED_PROXY_HOPS", 0)

type clientIPKey struct{}

// clientIPInfo is what the edge middleware could work out about the caller. It carries
// the "was a forwarding header present" flag as well as the address, because
// "hops=0 AND an XFF arrived" is exactly the misconfiguration that would otherwise let
// an administrator allowlist the load balancer's private address and believe they had
// restricted something (ADR 0047 decision 4).
type clientIPInfo struct {
	IP        netip.Addr
	OK        bool
	Forwarded bool // an X-Forwarded-For header was present on the request
}

// resolveClientIP applies the rule above to one request. Exported-by-test only.
func resolveClientIP(r *http.Request, hops int) clientIPInfo {
	fwd := forwardedChain(r)
	info := clientIPInfo{Forwarded: len(fwd) > 0}
	if hops <= 0 {
		// No proxy declared: the peer IS the client. Any XFF is a claim by that peer
		// and is deliberately not read.
		if ip, ok := parseHostIP(r.RemoteAddr); ok {
			info.IP, info.OK = ip, true
		}
		return info
	}
	// hops == 1 is the rightmost entry: the nearest proxy appended the address it
	// received the request from, which is the client itself.
	if idx := len(fwd) - hops; idx >= 0 && idx < len(fwd) {
		if ip, ok := parseHostIP(fwd[idx]); ok {
			info.IP, info.OK = ip, true
		}
		return info
	}
	// The chain is shorter than the deployment says it should be. That is not a client
	// we can name, and guessing (falling back to RemoteAddr, which is a trusted proxy)
	// would silently admit everyone.
	return info
}

// forwardedChain is every X-Forwarded-For entry in order, across repeated headers —
// net/http keeps them separate, and a proxy chain may use either form.
func forwardedChain(r *http.Request) []string {
	var out []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(h, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// parseHostIP accepts the three forms these fields actually arrive in: a bare address,
// "ip:port" (RemoteAddr always, and XFF when the load balancer is configured to append
// the client port), and "[v6]:port". An IPv4-mapped IPv6 address is unmapped so
// ::ffff:203.0.113.9 matches a 203.0.113.0/24 rule.
func parseHostIP(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.Addr{}, false
	}
	if ip, err := netip.ParseAddr(s); err == nil {
		return ip.Unmap(), true
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
			return ip.Unmap(), true
		}
	}
	return netip.Addr{}, false
}

// withClientIP is the outermost middleware: it resolves the caller once and puts the
// answer in the request context. Nothing downstream reads the forwarding headers
// itself — the same discipline authGate applies to the identity header, and for the
// same reason (a second reader is a second chance to trust the client's own claim).
func withClientIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := resolveClientIP(r, trustedProxyHops)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey{}, info)))
	})
}

// clientIPFrom reports what the edge worked out. The zero value (OK=false) means "we
// cannot name the caller", and every caller must treat that as a denial rather than as
// a pass — a restriction nobody can evaluate is not a restriction.
func clientIPFrom(ctx context.Context) clientIPInfo {
	info, _ := ctx.Value(clientIPKey{}).(clientIPInfo)
	return info
}

// parseCIDRList normalizes an operator-entered list into prefixes. A bare address
// becomes a single-host prefix (/32, /128). A prefix whose host bits are still set
// (192.0.2.7/24) is MASKED rather than rejected: it is the most common way to write
// "my office network", and silently keeping a value the matcher would treat
// differently is worse than either rejecting or rounding it — so it is rounded and the
// caller is told, by getting back the normalized text to display.
func parseCIDRList(s string) ([]netip.Prefix, []string, *apiError) {
	var prefixes []netip.Prefix
	var text []string
	for _, raw := range envx.SplitCSV(s) {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		var p netip.Prefix
		if ip, err := netip.ParseAddr(entry); err == nil {
			p = netip.PrefixFrom(ip.Unmap(), ip.Unmap().BitLen())
		} else {
			parsed, err := netip.ParsePrefix(entry)
			if err != nil {
				return nil, nil, &apiError{http.StatusBadRequest, "bad_cidr", "not an IP address or CIDR: " + entry}
			}
			p = parsed.Masked()
		}
		prefixes = append(prefixes, p)
		text = append(text, p.String())
	}
	return prefixes, text, nil
}

// ipInAny reports whether ip falls in one of the prefixes. An empty list is "no
// restriction" and is handled by the caller, not here.
func ipInAny(ip netip.Addr, prefixes []netip.Prefix) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap()
	for _, p := range prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIPBanner is the boot log line. "which of the two did this deployment mean" is
// unanswerable from the outside once it is running, and getting it wrong is silent.
func clientIPBanner() string {
	if trustedProxyHops <= 0 {
		return "trusted-proxy-hops=0 (client IP = peer address; X-Forwarded-For ignored)"
	}
	return "trusted-proxy-hops=" + strconv.Itoa(trustedProxyHops) +
		" (client IP = X-Forwarded-For counted from the right)"
}
