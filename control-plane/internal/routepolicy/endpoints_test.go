package routepolicy

import (
	"slices"
	"strings"
	"testing"
)

func list(v ...string) *[]string { return &v }

// A host pattern is spelled one way: lowercase, no trailing dot, punycode, canonical IP text. What
// would approve a whole public suffix, a port or path, or a wildcard anywhere but a leading "*."
// is refused.
func TestNormalizePattern(t *testing.T) {
	for in, want := range map[string]string{
		"LLM.Corp.Example.":  "llm.corp.example",
		" *.Corp.Example ":   "*.corp.example",
		"10.0.0.5":           "10.0.0.5",
		"[2001:DB8::1]":      "2001:db8::1",
		"::ffff:10.0.0.1":    "10.0.0.1",
		"bücher.example":     "xn--bcher-kva.example",
		"ollama":             "ollama",
		"*.internal":         "*.internal",
		"gpu.lab.corp.local": "gpu.lab.corp.local",
	} {
		if got, err := NormalizePattern(in, false); err != nil || got != want {
			t.Errorf("NormalizePattern(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "*", "*.", "llm.corp.example:8080", "http://llm.corp.example", "llm.corp.example/v1",
		"a.*.corp.example", "*.10.0.0.1", "*.com", "com", "co.uk", "*.co.uk", "*.github.io", "fe80::1%eth0"} {
		if got, err := NormalizePattern(in, false); err == nil {
			t.Errorf("NormalizePattern(%q) = %q, want a refusal", in, got)
		}
	}
	// The host name check refuses these too; the explicit checks say why.
	for in, why := range map[string]string{"*": "every host", "a.*.corp.example": "only a leading"} {
		if _, err := NormalizePattern(in, false); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("NormalizePattern(%q) = %v, want a refusal saying %q", in, err, why)
		}
	}
	if _, err := NormalizePattern("*.corp.example", true); err == nil {
		t.Error("a pattern was accepted where exact hosts are approved")
	}
	if got, err := NormalizePattern("LLM.corp.example", true); err != nil || got != "llm.corp.example" {
		t.Errorf("an exact host where exact hosts are approved: %q, %v", got, err)
	}
}

func TestCoversAndOverlap(t *testing.T) {
	for _, c := range []struct {
		outer, inner string
		want         bool
	}{
		{"*.corp.example", "llm.corp.example", true},
		{"*.corp.example", "corp.example", false},
		{"*.corp.example", "*.gpu.corp.example", true},
		{"*.gpu.corp.example", "*.corp.example", false},
		{"*.corp.example", "*.corp.example", true},
		{"llm.corp.example", "llm.corp.example", true},
		{"llm.corp.example", "*.corp.example", false},
		{"*.corp.example", "*.xcorp.example", false},
	} {
		if got := covers(c.outer, c.inner); got != c.want {
			t.Errorf("covers(%q, %q) = %v, want %v", c.outer, c.inner, got, c.want)
		}
	}
	if !overlap("*.gpu.corp.example", "*.corp.example") || overlap("*.a.example", "*.b.example") {
		t.Error("overlap")
	}
}

// No layer may name a host of a vendor destination, or a pattern covering one: an excluded vendor
// cannot be relabelled self-hosted (ADR 0138 §2).
func TestVendorHostsAreNeverApprovable(t *testing.T) {
	for _, p := range []string{"api.anthropic.com", "*.anthropic.com", "*.claude.ai", "chatgpt.com", "*.api.openai.com",
		"bedrock-runtime.us-east-1.amazonaws.com", "x.api.aws", "*.openrouter.ai"} {
		if err := (Rule{Endpoints: list(p)}).Validate(); err == nil || !strings.Contains(err.Error(), "vendor") {
			t.Errorf("endpoint %q: %v, want a vendor refusal", p, err)
		}
		if _, err := ParseCeiling("endpoints=" + p); err == nil {
			t.Errorf("a ceiling approving %q started", p)
		}
	}
	if err := (Rule{Endpoints: list("llm.corp.example", "*.gpu.corp.example")}).Validate(); err != nil {
		t.Errorf("own hosts: %v", err)
	}
	if err := (Rule{Destinations: list(SelfHosted)}).Validate(); err != nil {
		t.Errorf("the self-hosted destination is not in the vocabulary: %v", err)
	}
}

func TestParseCeilingEndpoints(t *testing.T) {
	r, err := ParseCeiling("destinations=aws-bedrock,self-hosted; endpoints=*.Corp.Example, llm.lab ,10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"*.corp.example", "10.0.0.5", "llm.lab"}; r.Endpoints == nil || !slices.Equal(*r.Endpoints, want) {
		t.Errorf("endpoints = %v, want %v", r.Endpoints, want)
	}
	for _, bad := range []string{"endpoints=*", "endpoints=*.com", "endpoints=llm.corp.example:8080"} {
		if _, err := ParseCeiling(bad); err == nil {
			t.Errorf("%q started", bad)
		}
	}
}

// A host is approved when the tenant rule names it and every layer above lets it through; the
// ceiling's unset list lets nothing through when it restricts destinations.
func TestApprovedEndpoints(t *testing.T) {
	tenant := Rule{Endpoints: list("llm.corp.example", "gpu.corp.example", "llm.other.example")}
	for _, c := range []struct {
		name string
		l    Layers
		want []string
	}{
		{"no layer narrows", Layers{Tenant: tenant}, []string{"llm.corp.example", "gpu.corp.example", "llm.other.example"}},
		{"a ceiling on classes only does not narrow", Layers{Ceiling: Rule{Classes: list(ClassSelfHosted)}, Tenant: tenant},
			[]string{"llm.corp.example", "gpu.corp.example", "llm.other.example"}},
		{"a ceiling restricting destinations with no endpoints lets none through",
			Layers{Ceiling: Rule{Destinations: list(SelfHosted)}, Tenant: tenant}, nil},
		{"the ceiling's patterns", Layers{Ceiling: Rule{Destinations: list(SelfHosted), Endpoints: list("*.corp.example")}, Tenant: tenant},
			[]string{"llm.corp.example", "gpu.corp.example"}},
		{"the deployment narrows", Layers{Ceiling: Rule{Endpoints: list("*.corp.example")}, Deployment: Rule{Endpoints: list("gpu.corp.example")}, Tenant: tenant},
			[]string{"gpu.corp.example"}},
		{"the cap narrows to none", Layers{TenantCap: Rule{Endpoints: list()}, Tenant: tenant}, nil},
		{"nothing approved", Layers{}, nil},
	} {
		if got := c.l.ApprovedEndpoints(); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	l := Layers{Ceiling: Rule{Destinations: list(SelfHosted), Endpoints: list("*.corp.example")}, TenantCap: Rule{Endpoints: list("gpu.corp.example")}}
	if by := l.EndpointExcludedBy("llm.other.example"); !slices.Equal(by, []string{LayerCeiling, LayerTenantCap}) {
		t.Errorf("excluded by %v", by)
	}
	if by := l.EndpointExcludedBy("api.openai.com"); len(by) == 0 || by[0] != "vendor" {
		t.Errorf("a vendor host excluded by %v", by)
	}
}

// A per-connection route on an approved endpoint is class self-hosted at destination self-hosted,
// judged by every layer's sets; a layer's deny still removes it.
func TestEndpointRoutes(t *testing.T) {
	both := []string{"custom-api", "member-llamacpp"}
	for _, c := range []struct {
		name string
		l    Layers
		want []string
	}{
		{"unrestricted", Layers{}, both},
		{"self-hosted destination allowed", Layers{Ceiling: Rule{Destinations: list("aws-bedrock", SelfHosted)}}, both},
		{"self-hosted destination not allowed", Layers{Tenant: Rule{Destinations: list("aws-bedrock")}}, nil},
		{"self-hosted class removed", Layers{Deployment: Rule{Classes: list("provided", "member-key")}}, nil},
		{"one route denied", Layers{TenantCap: Rule{Destinations: list(SelfHosted), Deny: []string{"member-llamacpp"}}}, []string{"custom-api"}},
	} {
		if got := c.l.EndpointRoutes(); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// The route id alone stays refused under a restriction: only an approved endpoint opens it.
	if (Layers{Ceiling: Rule{Destinations: list(SelfHosted)}}).Allows("member-llamacpp") {
		t.Error("member-llamacpp allowed by id")
	}
}

// A super_admin's patterns may not let through more than the ceiling's.
func TestEndpointsExceed(t *testing.T) {
	ceiling := Rule{Destinations: list(SelfHosted), Endpoints: list("*.corp.example")}
	if got := (Rule{Endpoints: list("*.gpu.corp.example", "x.corp.example")}).EndpointsExceed(ceiling); got != nil {
		t.Errorf("narrower patterns exceed: %v", got)
	}
	if got := (Rule{Endpoints: list("*.example", "x.corp.example")}).EndpointsExceed(ceiling); !slices.Equal(got, []string{"*.example"}) {
		t.Errorf("wider pattern: %v", got)
	}
	if got := (Rule{Endpoints: list("x.corp.example")}).EndpointsExceed(Rule{Destinations: list(SelfHosted)}); len(got) != 1 {
		t.Errorf("under a destination ceiling with no endpoints: %v", got)
	}
	if got := (Rule{Endpoints: list("x.corp.example")}).EndpointsExceed(Rule{}); got != nil {
		t.Errorf("under no ceiling: %v", got)
	}
	if got := (Rule{}).EndpointsExceed(ceiling); got != nil {
		t.Errorf("a layer that does not narrow exceeds: %v", got)
	}
}
