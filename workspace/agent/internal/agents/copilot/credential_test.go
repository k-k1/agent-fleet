package copilot

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

// copilot runs on the GitHub connection: a token renewal keeps the account and the binding,
// connecting another account changes it, and no connection leaves nothing usable.
func TestLoginBindsTheGitHubAccount(t *testing.T) {
	old := githubLogin
	t.Cleanup(func() { githubLogin = old })
	set := func(login string, ok bool) { githubLogin = func() (string, bool) { return login, ok } }

	set("octo", true)
	a := Login()
	if a.Route != routes.RouteCopilotLogin || a.Binding != credbind.Account(routes.RouteCopilotLogin, "octo") {
		t.Fatalf("Login = %+v", a)
	}
	if u := (agentImpl{}).UsableRoutes(); len(u) != 1 || u[0] != routes.RouteCopilotLogin {
		t.Errorf("usable = %v", u)
	}
	set("other", true)
	if Login().Binding == a.Binding {
		t.Error("another GitHub account kept the binding")
	}
	set("", false)
	if l := Login(); l.Route != routes.RouteCopilotLogin || l.Binding != "" {
		t.Errorf("no connection = %+v", l)
	}
	if u := (agentImpl{}).UsableRoutes(); u != nil {
		t.Errorf("usable without a connection = %v", u)
	}
}
