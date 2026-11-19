package routepolicy

import (
	"slices"
	"strings"
	"testing"
)

// The accepted spellings and what each is normalised to.
var normalizeCIDRAccepted = map[string]string{
	"10.20.0.0/16":        "10.20.0.0/16",
	" 10.0.0.0/8 ":        "10.0.0.0/8",
	"192.168.1.0/24":      "192.168.1.0/24",
	"172.16.0.0/12":       "172.16.0.0/12",
	"100.64.0.0/11":       "100.64.0.0/11",
	"fd12:3456:789a::/48": "fd12:3456:789a::/48",
	"FD12:3456:789A::/48": "fd12:3456:789a::/48",
	"fd12:0:0:0::/32":     "fd12::/32",
}

func TestNormalizePatternCIDR(t *testing.T) {
	for in, want := range normalizeCIDRAccepted {
		if want == "" {
			continue
		}
		got, err := NormalizePattern(in, false)
		if err != nil || got != want {
			t.Errorf("NormalizePattern(%q) = %q, %v; want %q", in, got, err, want)
			continue
		}
		if again, err := NormalizePattern(got, false); err != nil || again != got {
			t.Errorf("not idempotent: %q -> %q -> %q, %v", in, got, again, err)
		}
	}
	for in, why := range map[string]string{
		"0.0.0.0/0":           "private",
		"::/0":                "private",
		"10.0.0.0/7":          "private",
		"8.8.8.0/24":          "private",
		"2001:db8::/32":       "private",
		"127.0.0.0/8":         "private", // loopback
		"::1/128":             "one address",
		"169.254.0.0/16":      "private", // link-local, holds 169.254.169.254
		"169.254.169.254/32":  "one address",
		"169.254.169.0/24":    "private",
		"fe80::/10":           "private",
		"224.0.0.0/4":         "private",
		"fd00::/8":            "metadata", // holds fd00:ec2::254
		"fc00::/7":            "metadata",
		"fd00:ec2::/32":       "metadata",
		"100.64.0.0/10":       "metadata", // holds 100.100.100.200
		"fd20::/20":           "metadata", // GCP internal ULA band, holds fd20:ce::254
		"fd20:ce::/32":        "metadata",
		"fd00:ec2::/120":      "metadata", // holds the EKS Pod Identity address fd00:ec2::23
		"fd00:ec2::/64":       "metadata",
		"fd12:3456::%eth0/32": "",
		"::10.20.0.0/112":     "private", // IPv4-compatible
		"64:ff9b::/96":        "private", // NAT64
		"2002::/16":           "private", // 6to4
		"2001::/32":           "private", // Teredo
		"192.0.0.0/24":        "private",
		"168.63.129.0/24":     "private", // Azure wire server
		"10.20.5.7/16":        "host bits",
		"fd00:1::5/32":        "host bits",
		"10.0.0.5/32":         "one address",
		"fd12::1/128":         "one address",
		"::ffff:10.0.0.0/104": "mapped",
		"::ffff:0:0/96":       "mapped",
		"fe80::1%eth0/64":     "",
		"10.0.0.0/33":         "",
		"10.0.0.0/":           "",
		"10.0.0.0/-1":         "",
		"10.0.0.0/08":         "",
		"010.0.0.0/8":         "",
		"10.0.0.0/8/8":        "",
		"10.0.0.0/255.0.0.0":  "",
		"*.10.0.0.0/8":        "wildcard",
		"[fd12::]/32":         "",
		"http://10.0.0.0/8":   "",
		"10.0.0.0/8:80":       "",
		"llm.corp.example/8":  "",
	} {
		got, err := NormalizePattern(in, false)
		if err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("NormalizePattern(%q) = %q, %v; want a refusal saying %q", in, got, err, why)
		}
	}
	// The tenant rule approves hosts, not ranges.
	if got, err := NormalizePattern("10.20.0.0/16", true); err == nil {
		t.Errorf("a range was accepted where exact hosts are approved: %q", got)
	}
	if err := (Rule{Endpoints: list("10.20.0.0/16")}).ExactEndpoints(); err == nil {
		t.Error("ExactEndpoints accepted a range")
	}
}

func TestCIDRMatchesAndCovers(t *testing.T) {
	for _, c := range []struct {
		outer, inner string
		covers       bool
	}{
		{"10.20.0.0/16", "10.20.5.7", true},
		{"10.20.0.0/16", "10.20.0.0", true},
		{"10.20.0.0/16", "10.20.255.255", true},
		{"10.20.0.0/16", "10.21.0.1", false},
		{"10.20.0.0/16", "10.20.0.0/16", true},
		{"10.20.0.0/16", "10.20.8.0/24", true},
		{"10.20.8.0/24", "10.20.0.0/16", false}, // narrower never covers wider
		{"10.20.0.0/16", "10.21.0.0/24", false},
		{"10.0.0.0/8", "10.20.0.0/16", true},
		{"10.20.0.0/16", "*.corp.example", false},
		{"10.20.0.0/16", "llm.corp.example", false}, // names are never resolved
		{"10.20.0.0/16", "10.20.5.7.nip.io", false},
		{"*.corp.example", "10.20.0.0/16", false},
		{"llm.corp.example", "10.20.0.0/16", false},
		{"10.20.5.7", "10.20.0.0/16", false},
		{"10.20.5.7", "10.20.5.7", true},
		{"fd12:3456::/32", "fd12:3456::5", true},
		{"fd12:3456::/32", "fd12:3457::5", false},
		{"fd12:3456::/32", "fd12:3456:1::/48", true},
		{"fd12:3456:1::/48", "fd12:3456::/32", false},
		{"fd12:3456::/32", "10.20.5.7", false}, // families never mix
		{"10.20.0.0/16", "fd12:3456::5", false},
		{"10.20.0.0/16", "::ffff:10.20.5.7", true}, // an IPv4-mapped host is the IPv4 address
		{"10.20.0.0/16", "::ffff:10.21.5.7", false},
		{"10.20.0.0/16", "fe80::1%eth0", false},
		{"fd12:3456::/32", "fd12:3456::1%eth0", false},
		{"10.20.0.0/16", "010.20.5.7", false},
		{"10.20.0.0/16", "0xa.20.5.7", false},
		{"10.20.0.0/16", "169084167", false},
		{"::ffff:10.20.0.0/112", "::ffff:10.20.0.0/120", false},
		// Non-canonical patterns match nothing rather than something wider.
		{"10.20.5.7/16", "10.20.5.8", false},
		{"10.20.0.0/16", "10.20.5.7/16", false},
		{"::ffff:10.20.0.0/112", "10.20.5.7", false},
		{"10.20.0.0/16", "", false},
	} {
		if got := covers(c.outer, c.inner); got != c.covers {
			t.Errorf("covers(%q, %q) = %v, want %v", c.outer, c.inner, got, c.covers)
		}
	}
	if !overlap("10.0.0.0/8", "10.20.0.0/16") || overlap("10.20.0.0/16", "10.21.0.0/16") {
		t.Error("overlap of ranges")
	}
}

// A range in a layer above lets exact IPs through, and only IP literals; a narrower layer is held
// inside it.
func TestCIDRLayers(t *testing.T) {
	tenant := Rule{Endpoints: list("10.20.5.7", "10.21.0.1", "llm.corp.example", "fd12:3456::5")}
	l := Layers{Ceiling: Rule{Destinations: list(SelfHosted), Endpoints: list("10.20.0.0/16", "fd12:3456::/32")}, Tenant: tenant}
	if got, want := l.ApprovedEndpoints(), []string{"10.20.5.7", "fd12:3456::5"}; !slices.Equal(got, want) {
		t.Errorf("approved = %v, want %v", got, want)
	}
	if by := l.EndpointExcludedBy("llm.corp.example"); !slices.Equal(by, []string{LayerCeiling}) {
		t.Errorf("a host name that might resolve into the range: excluded by %v", by)
	}

	ceiling := Rule{Destinations: list(SelfHosted), Endpoints: list("10.20.0.0/16", "*.corp.example")}
	for _, c := range []struct {
		name  string
		rule  []string
		wider []string
	}{
		{"inside", []string{"10.20.8.0/24", "10.20.1.1", "*.corp.example"}, nil},
		{"wider range", []string{"10.0.0.0/8"}, []string{"10.0.0.0/8"}},
		{"outside", []string{"10.21.0.0/24", "10.21.0.1", "192.168.0.0/16"}, []string{"10.21.0.0/24", "10.21.0.1", "192.168.0.0/16"}},
		{"a range under a suffix", []string{"10.20.0.0/16"}, nil},
	} {
		got := (Rule{Endpoints: &c.rule}).EndpointsExceed(ceiling)
		if !slices.Equal(got, c.wider) {
			t.Errorf("%s: exceeds %v, want %v", c.name, got, c.wider)
		}
	}
	// A suffix pattern under a ceiling that names only a range is outside it.
	if got := (Rule{Endpoints: list("*.corp.example")}).EndpointsExceed(Rule{Endpoints: list("10.0.0.0/8")}); len(got) != 1 {
		t.Errorf("a suffix under a range ceiling: %v", got)
	}
}

func TestCIDRRuleNormalizeAndCeiling(t *testing.T) {
	r, err := ParseCeiling("destinations=self-hosted;endpoints=10.20.8.0/24, FD12:3456::/32, 10.20.0.0/16,10.20.0.0/16, *.corp.example")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"*.corp.example", "10.20.0.0/16", "10.20.8.0/24", "fd12:3456::/32"}
	if r.Endpoints == nil || !slices.Equal(*r.Endpoints, want) {
		t.Errorf("endpoints = %v, want %v", r.Endpoints, want)
	}
	if n := r.Normalize(); !slices.Equal(*n.Endpoints, want) {
		t.Errorf("Normalize is not idempotent: %v", *n.Endpoints)
	}
	for _, bad := range []string{"endpoints=0.0.0.0/0", "endpoints=169.254.0.0/16", "endpoints=10.20.5.7/16", "endpoints=fd00::/8"} {
		if _, err := ParseCeiling(bad); err == nil {
			t.Errorf("%q started", bad)
		}
	}
}
