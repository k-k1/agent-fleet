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

// DialControl is a net.Dialer.Control hook that refuses AddrRefused addresses. It runs on
// the resolved address about to be connected, so the address that is judged is the one that
// is dialled; a check made on a separate lookup of the name can disagree with the lookup
// the dial performs. Every outbound request whose destination a tenant or an operator typed
// goes through it.
func DialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return ErrRefusedAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || AddrRefused(ip) {
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
