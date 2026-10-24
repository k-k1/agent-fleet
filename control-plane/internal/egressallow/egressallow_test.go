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
