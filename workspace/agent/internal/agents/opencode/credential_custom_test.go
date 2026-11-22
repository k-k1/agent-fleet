package opencode

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

func withCustomConn(t *testing.T, c *secrets.CustomAPIConn) {
	t.Helper()
	old := customConn
	t.Cleanup(func() { customConn = old })
	customConn = func() (secrets.CustomAPIConn, bool) {
		if c == nil {
			return secrets.CustomAPIConn{}, false
		}
		return *c, true
	}
}

// Without a stored connection custom-api is exactly what it was: no binding, never on an
// approved endpoint.
func TestCustomAPIWithoutStoredConn(t *testing.T) {
	withCustomConn(t, nil)
	l := resolveModel("mine/some-model", nil, UsageZen, oauthState{}, nil)
	if l.Route != routes.RouteCustomAPI || l.Binding != "" {
		t.Fatalf("launch = %+v", l)
	}
	p := routepolicy.Policy{Restricted: true, Endpoints: []string{"10.0.0.5"}, EndpointRoutes: []string{routes.RouteCustomAPI}}
	if p.Allows(routes.RouteCustomAPI) {
		t.Fatal("custom-api allowed on an approved endpoint with no stored connection")
	}
}

func TestCustomAPIOnStoredConn(t *testing.T) {
	withCustomConn(t, &secrets.CustomAPIConn{Provider: "mine", URL: "http://10.0.0.5:8080/v1", APIKey: "k"})
	l := resolveModel("mine/some-model", nil, UsageZen, oauthState{}, nil)
	if want := credbind.Endpoint(routes.RouteCustomAPI, "http://10.0.0.5:8080/v1", "k"); l.Binding == "" || l.Binding != want {
		t.Fatalf("binding = %q, want %q", l.Binding, want)
	}
	if o := resolveModel("other/some-model", nil, UsageZen, oauthState{}, nil); o.Binding != "" {
		t.Fatalf("another custom provider got the stored connection's binding: %+v", o)
	}
	// Repointing the URL or replacing the key is another connection.
	withCustomConn(t, &secrets.CustomAPIConn{Provider: "mine", URL: "http://10.0.0.6:8080/v1", APIKey: "k"})
	if r := resolveModel("mine/m", nil, UsageZen, oauthState{}, nil); r.Binding == l.Binding {
		t.Fatal("binding did not change with the URL")
	}

	on := routepolicy.Policy{Restricted: true, Endpoints: []string{"10.0.0.5"}, EndpointRoutes: []string{routes.RouteCustomAPI}}
	withCustomConn(t, &secrets.CustomAPIConn{Provider: "mine", URL: "http://10.0.0.5:8080/v1"})
	if !on.Allows(routes.RouteCustomAPI) {
		t.Fatal("custom-api refused on an approved endpoint")
	}
	withCustomConn(t, &secrets.CustomAPIConn{Provider: "mine", URL: "http://10.9.9.9:8080/v1"})
	if on.Allows(routes.RouteCustomAPI) {
		t.Fatal("custom-api allowed on an unapproved endpoint")
	}
}
