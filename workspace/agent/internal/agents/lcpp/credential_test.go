package lcpp

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// The member's own endpoint wins over the engine; repointing it or replacing its key is another
// connection, and the endpoint never appears in the binding.
func TestConnection(t *testing.T) {
	old := memberConn
	t.Cleanup(func() { memberConn = old })
	set := func(c secrets.LcppConn, ok bool) { memberConn = func() (secrets.LcppConn, bool) { return c, ok } }

	set(secrets.LcppConn{}, false)
	if l := Connection(); l != (routes.Launch{Route: routes.RouteAFEngineLLM, Binding: "af-engine-llm@llm"}) {
		t.Errorf("engine = %+v", l)
	}
	set(secrets.LcppConn{URL: "http://gpu.example:8080/", APIKey: "k1"}, true)
	a := Connection()
	if a.Route != routes.RouteMemberLlamacpp || a.Binding == "" || len(a.Binding) != len(routes.RouteMemberLlamacpp)+17 {
		t.Fatalf("member = %+v", a)
	}
	set(secrets.LcppConn{URL: "http://gpu.example:8080", APIKey: "k1"}, true)
	if Connection() != a {
		t.Error("a trailing slash changed the connection")
	}
	set(secrets.LcppConn{URL: "http://gpu.example:8080", APIKey: "k2"}, true)
	if Connection().Binding == a.Binding {
		t.Error("a new key kept the binding")
	}
	set(secrets.LcppConn{URL: "http://other.example:8080", APIKey: "k1"}, true)
	if Connection().Binding == a.Binding {
		t.Error("another endpoint kept the binding")
	}
}
