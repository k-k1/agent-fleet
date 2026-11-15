package sessionx

import (
	"net/http"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/lcpp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// setLcppURL stores the member's llama.cpp connection.
func setLcppURL(t *testing.T, url string) {
	t.Helper()
	if err := secrets.Update(func(s *secrets.Data) error { s.Lcpp = &secrets.LcppConn{URL: url}; return nil }); err != nil {
		t.Fatal(err)
	}
}

// Under a restriction an lcpp session on the member's endpoint is created, started and kept
// running only while that endpoint's host is approved (ADR 0138 §2), through the real lcpp
// connection: a create on another host is refused, a stored launch whose URL changed since is a
// replacement, and the sweep halts a running one whose endpoint moved off the approved host.
func TestLcppSessionsOnAnApprovedEndpoint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	p := noSubscriptions
	p.Endpoints, p.EndpointRoutes = []string{"llm.corp.example"}, []string{routes.RouteMemberLlamacpp}
	usePolicy(t, p)

	setLcppURL(t, "http://LLM.corp.example:8081")
	m := session.Meta{Name: "l1", Kind: session.KindLcpp, Dir: t.TempDir()}
	ref, rec := newLaunchPolicyRefusal(session.KindLcpp, nil, m)
	if ref != nil || rec == nil || rec.Route != routes.RouteMemberLlamacpp {
		t.Fatalf("a create on the approved endpoint: %+v, launch %+v", ref, rec)
	}
	m.LaunchRoute = rec
	if ref := storedLaunchPolicyRefusal(m); ref != nil {
		t.Errorf("a start on the same endpoint: %+v", ref)
	}
	if ref := runningPolicyRefusal(m); ref != nil {
		t.Errorf("the sweep on the same endpoint: %+v", ref)
	}

	setLcppURL(t, "http://llm.other.example:8081")
	ref, _ = newLaunchPolicyRefusal(session.KindLcpp, nil, session.Meta{Name: "l2", Kind: session.KindLcpp})
	wantPolicyRefusal(t, "a create on a host that is not approved", ref, http.StatusForbidden, routes.CodePolicyForbidden)
	wantPolicyRefusal(t, "a start after the endpoint moved off the approved host", storedLaunchPolicyRefusal(m), http.StatusForbidden, routes.CodePolicyForbidden)
	wantPolicyRefusal(t, "the sweep after the endpoint moved off the approved host", runningPolicyRefusal(m), http.StatusForbidden, routes.CodePolicyForbidden)

	// Another URL on the same approved host is another connection: a replacement, never moved.
	setLcppURL(t, "http://llm.corp.example:9090")
	if got := lcpp.Connection(); got.Binding == rec.Binding {
		t.Fatal("the premise: another URL binds differently")
	}
	wantPolicyRefusal(t, "a start on another URL of the approved host", storedLaunchPolicyRefusal(m), http.StatusConflict, routes.CodePolicyForbidden)
}
