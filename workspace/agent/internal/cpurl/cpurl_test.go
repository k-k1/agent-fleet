package cpurl

import (
	"reflect"
	"testing"
)

func TestRequestPrefersInternalOnlyAlongsidePublic(t *testing.T) {
	for _, c := range []struct{ pub, in, wantReq string }{
		{"", "", ""},
		{"https://af.example/", "", "https://af.example"},
		{" https://af.example ", "http://af-cp-internal.ns.svc:8098/", "http://af-cp-internal.ns.svc:8098"},
		// The bridge tokens come only with the public base, so an internal URL alone
		// is not a CP to ask.
		{"", "http://af-cp-internal.ns.svc:8098", ""},
	} {
		t.Setenv("AF_CP_BASE_URL", c.pub)
		t.Setenv("AF_CP_INTERNAL_URL", c.in)
		if got := Request(); got != c.wantReq {
			t.Errorf("Request() with base=%q internal=%q = %q, want %q", c.pub, c.in, got, c.wantReq)
		}
	}
}

func TestPublicIgnoresInternal(t *testing.T) {
	t.Setenv("AF_CP_BASE_URL", "https://af.example/")
	t.Setenv("AF_CP_INTERNAL_URL", "http://af-cp-internal.ns.svc:8098")
	if got := Public(); got != "https://af.example" {
		t.Errorf("Public() = %q", got)
	}
}

func TestAllNamesBothOnce(t *testing.T) {
	t.Setenv("AF_CP_BASE_URL", "https://af.example")
	t.Setenv("AF_CP_INTERNAL_URL", "")
	if got := All(); !reflect.DeepEqual(got, []string{"https://af.example"}) {
		t.Errorf("All() unset = %q", got)
	}
	t.Setenv("AF_CP_INTERNAL_URL", "http://af-cp-internal.ns.svc:8098")
	if got := All(); !reflect.DeepEqual(got, []string{"https://af.example", "http://af-cp-internal.ns.svc:8098"}) {
		t.Errorf("All() = %q", got)
	}
	t.Setenv("AF_CP_INTERNAL_URL", "https://af.example/")
	if got := All(); !reflect.DeepEqual(got, []string{"https://af.example"}) {
		t.Errorf("All() duplicate = %q", got)
	}
}
