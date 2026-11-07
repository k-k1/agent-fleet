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
