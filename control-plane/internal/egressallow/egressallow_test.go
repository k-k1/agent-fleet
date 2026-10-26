package egressallow

import (
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
