package routepolicy

import "testing"

const testEndpointRoute = "test-per-connection"

// stubEndpoint registers the host testEndpointRoute's connection points at, counting the reads.
func stubEndpoint(t *testing.T, host *string) *int {
	t.Helper()
	reads := 0
	RegisterEndpoint(testEndpointRoute, func() string { reads++; return *host })
	t.Cleanup(func() {
		endpointMu.Lock()
		delete(endpointHosts, testEndpointRoute)
		endpointMu.Unlock()
	})
	return &reads
}

// Under a restriction a per-connection route is allowed only while its connection points at an
// approved host and the route is one allowed there; with no restriction it is allowed as before.
func TestAllowsOnAnApprovedEndpoint(t *testing.T) {
	host := "LLM.Corp.Example."
	reads := stubEndpoint(t, &host)
	p := Policy{Revision: 2, Restricted: true, Allowed: []string{"anthropic-api"},
		Endpoints: []string{"llm.corp.example", "10.0.0.5"}, EndpointRoutes: []string{testEndpointRoute}}
	if !p.Allows(testEndpointRoute) {
		t.Error("the route on an approved host was refused")
	}
	host = "llm.other.example"
	if p.Allows(testEndpointRoute) {
		t.Error("the route on a host that is not approved was allowed")
	}
	host = "[::ffff:10.0.0.5]"
	if !p.Allows(testEndpointRoute) {
		t.Error("the route on an approved IP address was refused")
	}
	host = ""
	if p.Allows(testEndpointRoute) {
		t.Error("the route with no connection was allowed")
	}
	host = "llm.corp.example"
	if q := (Policy{Restricted: true, Endpoints: p.Endpoints}); q.Allows(testEndpointRoute) {
		t.Error("allowed on an approved host although no layer allows the route there")
	}
	if (Policy{Restricted: true, Endpoints: p.Endpoints, EndpointRoutes: []string{"unregistered"}}).Allows("unregistered") {
		t.Error("a route with no registered connection was allowed")
	}
	before := *reads
	_ = (Policy{Restricted: true}).Allows(testEndpointRoute)
	_ = (Policy{Restricted: true, Endpoints: p.Endpoints}).Allows("anthropic-api")
	if *reads != before {
		t.Error("the connection was read for a policy that approves no endpoint for the route")
	}
	if !(Policy{}).Allows(testEndpointRoute) {
		t.Error("an unrestricted policy refused the route")
	}
	if !p.AllowsAt(testEndpointRoute, "llm.corp.example") || p.AllowsAt(testEndpointRoute, "llm.other.example") {
		t.Error("AllowsAt does not judge the host it is given")
	}
}

func TestCanonicalHost(t *testing.T) {
	for in, want := range map[string]string{
		"LLM.Corp.Example.": "llm.corp.example",
		"[2001:DB8::1]":     "2001:db8::1",
		"::ffff:10.0.0.1":   "10.0.0.1",
		"fe80::1%eth0":      "",
		"":                  "",
	} {
		if got := CanonicalHost(in); got != want {
			t.Errorf("CanonicalHost(%q) = %q, want %q", in, got, want)
		}
	}
	if got := HostOf("http://LLM.corp.example:8080/v1"); got != "LLM.corp.example" {
		t.Errorf("HostOf = %q", got)
	}
}
