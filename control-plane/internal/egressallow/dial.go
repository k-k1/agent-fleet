package egressallow

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// ErrRefusedAddr is what DialControl returns for an address AddrRefused names.
var ErrRefusedAddr = errors.New("egress to an internal address is not allowed")

// ProxyAddrRefused is the explicit forward proxy's narrower set: loopback (the proxy host
// itself), link-local (the cloud metadata address included) and unspecified. Private ranges are
// deliberately not here: workspaces legitimately reach internal Git, MCP and cluster services
// through the proxy, and section 7.8 promises only these three. An IPv4-mapped IPv6 address is
// judged as the IPv4 address it names.
func ProxyAddrRefused(a netip.Addr) bool {
	a = a.Unmap().WithZone("")
	return a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsUnspecified()
}

// ProxyDialControl is DialControl with ProxyAddrRefused, for the forward proxy and for the
// engine-ingest look-ahead. Tenant-typed issuers and the gateway relay use the broader DialControl.
func ProxyDialControl(_, address string, _ syscall.RawConn) error {
	return dialControl(address, ProxyAddrRefused)
}

// DialControl is a net.Dialer.Control hook that refuses AddrRefused addresses. It runs on
// the resolved address about to be connected, so the address that is judged is the one that
// is dialled; a check made on a separate lookup of the name can disagree with the lookup
// the dial performs. Every outbound request whose destination a tenant or an operator typed
// goes through it.
func DialControl(_, address string, _ syscall.RawConn) error {
	return dialControl(address, AddrRefused)
}

func dialControl(address string, refused func(netip.Addr) bool) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return ErrRefusedAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || refused(ip) {
		return ErrRefusedAddr
	}
	return nil
}

// NewPublicOnlyClient returns an HTTP client that connects to public addresses only (see
// DialControl), ignores proxy environment variables and never follows a redirect: a 3xx is
// returned to the caller as the response, which the callers read as a failure.
func NewPublicOnlyClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         (&net.Dialer{Timeout: timeout, Control: DialControl}).DialContext,
			TLSHandshakeTimeout: timeout,
			MaxIdleConns:        10,
			IdleConnTimeout:     30 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// AttachedDialControl is ProxyDialControl that also refuses every address inside a network
// the proxy host itself sits on. On the docker fence the proxy container is attached to
// every per-user workspace network, so without this one workspace reaches another's
// container through the proxy, which the per-user networks exist to prevent. prefixes is
// read on every dial: networks are attached and detached while the proxy runs.
func AttachedDialControl(prefixes func() []netip.Prefix) func(string, string, syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		return dialControl(address, func(a netip.Addr) bool {
			if ProxyAddrRefused(a) {
				return true
			}
			a = a.Unmap().WithZone("")
			for _, p := range prefixes() {
				if p.Contains(a) {
					return true
				}
			}
			return false
		})
	}
}

// InterfacePrefixes is the networks this host has an address on. When the interfaces
// cannot be read, or one of them cannot be understood, it answers every address, so
// AttachedDialControl refuses rather than letting a workspace network through.
func InterfacePrefixes() []netip.Prefix {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return everyPrefix
	}
	return prefixesOf(addrs)
}

var everyPrefix = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")}

func prefixesOf(addrs []net.Addr) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(addrs))
	for _, a := range addrs {
		p, err := netip.ParsePrefix(a.String())
		if err != nil {
			return everyPrefix
		}
		out = append(out, p.Masked())
	}
	return out
}
