package mcpsrv

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// routesCP answers AllowedRoutes per tenant.
type routesCP struct {
	*memCP
	byTenant map[string]map[string][]string
	err      error
}

func (c routesCP) AllowedRoutes(_ context.Context, tenantID string) (map[string][]string, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.byTenant[tenantID], nil
}

var allClaudeRoutes = []string{"anthropic-subscription", "anthropic-api", "bedrock", "bedrock-deployment"}

// create_session's route text names only the routes the caller's tenant policy allows (ADR 0138
// D10a, #4323): a forbidden route is left out, the Bedrock clause goes with the last Bedrock route,
// another tenant keeps its own routes, and an unreadable policy names none.
func TestCreateRouteTextNamesOnlyAllowedRoutes(t *testing.T) {
	base := newMemCP(t, memStore(t), "running")
	prin := mcpPrincipal{identityID: base.prin.identityID, membershipID: base.prin.membershipID, scope: "write"}
	routeText := func(cp CP) string {
		return toolTexts(t, New(cp).toolsFor(context.Background(), &prin), "create_session")
	}
	allowed := func(m map[string][]string) routesCP {
		return routesCP{memCP: base, byTenant: map[string]map[string][]string{base.tenantID: m}}
	}
	routeNames := append([]string{"opencode accepts bedrock"}, allClaudeRoutes...)

	cases := []struct {
		name    string
		cp      CP
		named   []string
		unnamed []string
		bedrock bool
	}{
		{"nothing forbidden", allowed(map[string][]string{"claude": allClaudeRoutes, "opencode": {"bedrock"}}), routeNames, nil, true},
		{"bedrock forbidden", allowed(map[string][]string{"claude": {"anthropic-subscription", "anthropic-api"}, "opencode": nil}),
			[]string{"anthropic-subscription", "anthropic-api"}, []string{"bedrock", "opencode accepts"}, false},
		{"only the subscription", allowed(map[string][]string{"claude": {"anthropic-subscription"}}),
			[]string{"claude accepts anthropic-subscription"}, []string{"anthropic-api", "bedrock"}, false},
		{"unreadable policy", routesCP{memCP: base, err: errors.New("database is locked")},
			nil, append([]string{"accepts"}, allClaudeRoutes...), false},
	}
	for _, c := range cases {
		text := routeText(c.cp)
		for _, n := range c.named {
			if !strings.Contains(text, n) {
				t.Errorf("%s: route text lacks %q:\n%s", c.name, n, text)
			}
		}
		for _, n := range c.unnamed {
			if strings.Contains(text, n) {
				t.Errorf("%s: route text names the forbidden %q:\n%s", c.name, n, text)
			}
		}
		if got := strings.Contains(text, "acceptance switch"); got != c.bedrock {
			t.Errorf("%s: acceptance-switch clause present=%v, want %v", c.name, got, c.bedrock)
		}
	}

	// Another tenant, with another policy, is not served the first one's text.
	other := routesCP{memCP: base, byTenant: map[string]map[string][]string{
		base.tenantID: {"claude": {"anthropic-api"}},
		"other":       {"claude": allClaudeRoutes},
	}}
	if text := routeText(other); strings.Contains(text, "bedrock") {
		t.Errorf("tenant text names bedrock:\n%s", text)
	}
	if text := New(other).routeDescFor(t, "other"); !strings.Contains(text, "bedrock-deployment") {
		t.Errorf("other tenant lost its routes: %s", text)
	}
}

// routeDescFor is create_session's route description for a tenant id, built the way toolsFor does.
func (a API) routeDescFor(t *testing.T, tenantID string) string {
	t.Helper()
	byKind, err := a.cp.AllowedRoutes(context.Background(), tenantID)
	if err != nil {
		t.Fatal(err)
	}
	r := allowedRoutes{}
	for k, rs := range byKind {
		r[k] = map[string]bool{}
		for _, x := range rs {
			r[k][x] = true
		}
	}
	return createRouteDesc(nil, r)
}
