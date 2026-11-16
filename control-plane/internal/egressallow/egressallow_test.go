package egressallow

import (
	"errors"
	"net"
	"net/netip"
	"reflect"
	"testing"
)

func TestMinimalAllowsTheSame(t *testing.T) {
	entries := []string{
		"api.anthropic.com", ".anthropic.com", "claude.ai", ".claude.ai", "*.example.org",
		".a.example.org", "example.org", "github.com", ".github.com", ".githubusercontent.com",
		"pypi.org", ".pypi.org.evil", ".x.com", ".x.com",
	}
	p := New(entries)
	got := p.Minimal()
	want := []string{".anthropic.com", ".claude.ai", ".example.org", ".github.com", ".githubusercontent.com", ".pypi.org.evil", ".x.com", "pypi.org"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Minimal() = %v\nwant %v", got, want)
	}
	m := New(got)
	for _, h := range []string{
		"api.anthropic.com", "anthropic.com", "claude.ai", "b.a.example.org", "example.org",
		"github.com", "x.githubusercontent.com", "pypi.org", "x.pypi.org", "evil.com", "x.com", "y.x.com",
	} {
		if p.Allows(h) != m.Allows(h) {
			t.Fatalf("%s: full policy says %v, minimal says %v", h, p.Allows(h), m.Allows(h))
		}
	}
}

func TestCanonicalIPv6(t *testing.T) {
	for in, want := range map[string]string{
		"2001:DB8:0:0:0:0:0:5": "2001:db8::5",
		"[2001:db8::5]":        "2001:db8::5",
		"fd00::0007":           "fd00::7",
	} {
		if got, ok := CanonicalIPv6(in); !ok || got != want {
			t.Errorf("CanonicalIPv6(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "10.0.1.5", "example.com", "fe80::1%eth0", "::ffff:10.0.1.5",
		"::", "::1", "::192.0.2.5", "::c000:205", "fe80::1", "ff02::1", "[2001:db8::5", "2001:db8::5]:443"} {
		if got, ok := CanonicalIPv6(in); ok {
			t.Errorf("CanonicalIPv6(%q) = %q, want refusal", in, got)
		}
	}
}

// A listed IPv6 address is allowed however the caller spells it (bare, bracketed, expanded,
// upper case), and its neighbour is not.
func TestAllowsIPv6InAnySpelling(t *testing.T) {
	p := New([]string{"2001:db8::5", "github.com"})
	for _, h := range []string{"2001:db8::5", "[2001:db8::5]", "[2001:DB8:0:0:0:0:0:5]", "2001:0db8::0005"} {
		if !p.Allows(h) {
			t.Errorf("Allows(%q) = false, want true", h)
		}
	}
	for _, h := range []string{"2001:db8::6", "[2001:db8::6]", "2001:db8::", "::1"} {
		if p.Allows(h) {
			t.Errorf("Allows(%q) = true, want false", h)
		}
	}
}

// The docker fence attaches the proxy to every per-user network. A destination on any of
// them is refused, a private address elsewhere is still served, and the narrower set
// (loopback, link-local) stays refused.
func TestAttachedDialControl(t *testing.T) {
	attached := []netip.Prefix{netip.MustParsePrefix("172.20.0.0/16"), netip.MustParsePrefix("fd00:1::/64")}
	control := AttachedDialControl(func() []netip.Prefix { return attached })
	for _, tc := range []struct {
		addr    string
		refused bool
	}{
		{"172.20.0.3:7700", true},
		{"[::ffff:172.20.0.3]:7700", true},
		{"[fd00:1::5]:80", true},
		{"172.17.0.1:8098", false},
		{"10.1.2.3:443", false},
		{"93.184.216.34:443", false},
		{"127.0.0.1:80", true},
		{"169.254.169.254:80", true},
		{"not-an-address", true},
	} {
		err := control("tcp", tc.addr, nil)
		if got := errors.Is(err, ErrRefusedAddr); got != tc.refused {
			t.Errorf("%s: refused = %v, want %v", tc.addr, got, tc.refused)
		}
	}
	// A network attached after the control was built is refused too.
	attached = append(attached, netip.MustParsePrefix("172.21.0.0/16"))
	if err := control("tcp", "172.21.0.9:22", nil); !errors.Is(err, ErrRefusedAddr) {
		t.Errorf("a network attached later is not refused: %v", err)
	}
}

func TestInterfacePrefixesCoverLoopback(t *testing.T) {
	lo := netip.MustParseAddr("127.0.0.1")
	for _, p := range InterfacePrefixes() {
		if p.Contains(lo) {
			return
		}
	}
	t.Errorf("InterfacePrefixes() = %v: the loopback network is missing", InterfacePrefixes())
}

// An interface address that cannot be read as a prefix must not open its network.
func TestPrefixesOfFailsClosed(t *testing.T) {
	_, ws, _ := net.ParseCIDR("172.20.0.2/16")
	got := prefixesOf([]net.Addr{ws})
	if len(got) != 1 || got[0] != netip.MustParsePrefix("172.20.0.0/16") {
		t.Fatalf("prefixesOf = %v", got)
	}
	got = prefixesOf([]net.Addr{ws, &net.UnixAddr{Name: "/run/x", Net: "unix"}})
	for _, a := range []string{"172.21.0.9", "93.184.216.34", "2001:db8::1"} {
		in := false
		for _, p := range got {
			in = in || p.Contains(netip.MustParseAddr(a))
		}
		if !in {
			t.Errorf("%s is not refused after an unreadable interface address (%v)", a, got)
		}
	}
}
