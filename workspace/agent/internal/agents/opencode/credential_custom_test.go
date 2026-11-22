package opencode

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
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

// The production reader, on a real store: a store without the key (an older file) reads as no
// connection, and a stored one is read back under its JSON name and bound.
func TestCustomAPIFromTheStore(t *testing.T) {
	isolatedHome(t)
	t.Setenv("AF_SECRET_KEY", "")
	if _, ok := customConn(); ok {
		t.Fatal("a connection read from an empty store")
	}
	if l := resolveModel("mine/m", nil, UsageZen, oauthState{}, nil); l.Route != routes.RouteCustomAPI || l.Binding != "" {
		t.Fatalf("launch without a stored connection = %+v", l)
	}
	if err := secrets.Update(func(d *secrets.Data) error {
		d.CustomAPI = &secrets.CustomAPIConn{Provider: "mine", URL: "http://10.0.0.5:8080/v1", APIKey: "k"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c, ok := customConn()
	if !ok || c.Provider != "mine" || c.URL != "http://10.0.0.5:8080/v1" {
		t.Fatalf("stored connection read back as %+v ok=%v", c, ok)
	}
	want := credbind.Endpoint(routes.RouteCustomAPI, c.URL, "k")
	if l := resolveModel("mine/m", nil, UsageZen, oauthState{}, nil); l.Binding != want {
		t.Fatalf("binding = %q, want %q", l.Binding, want)
	}
	if l := resolveModel("other/m", nil, UsageZen, oauthState{}, nil); l.Binding != "" {
		t.Fatalf("another provider bound: %+v", l)
	}
}

func TestCustomAPIBinding(t *testing.T) {
	withCustomConn(t, &secrets.CustomAPIConn{Provider: "mine", URL: "http://10.0.0.5:8080/v1", APIKey: "k"})
	l := resolveModel("mine/some-model", nil, UsageZen, oauthState{}, nil)
	if want := credbind.Endpoint(routes.RouteCustomAPI, "http://10.0.0.5:8080/v1", "k"); l.Binding == "" || l.Binding != want {
		t.Fatalf("binding = %q, want %q", l.Binding, want)
	}
	// Repointing the URL is another connection.
	withCustomConn(t, &secrets.CustomAPIConn{Provider: "mine", URL: "http://10.0.0.6:8080/v1", APIKey: "k"})
	if r := resolveModel("mine/m", nil, UsageZen, oauthState{}, nil); r.Binding == l.Binding {
		t.Fatal("binding did not change with the URL")
	}
}
