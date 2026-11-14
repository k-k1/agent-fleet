package muse

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

// Both login modes resolve to their own route; a refresh keeps the binding, another account or
// another key changes it, and META_API_KEY in the environment wins over the login (ADR 0138 D2).
func TestLoginModes(t *testing.T) {
	t.Setenv("META_API_KEY", "")
	account := func(email, token string) string {
		return `{"schema_version":1,"providers":{"meta":{"access_token":"` + token + `","api_key":"k-` + token +
			`","mechanism":"oauth","obtained_via":"device_code","user_email":"` + email + `"}}}`
	}
	a := loginOf([]byte(account("a@example.com", "t1")))
	if a.Route != routes.RouteMuseLogin || a.Binding == "" || strings.Contains(a.Binding, "example") {
		t.Fatalf("account login = %+v", a)
	}
	if r := loginOf([]byte(account("a@example.com", "t2"))); r != a {
		t.Errorf("a token refresh changed the launch: %+v vs %+v", r, a)
	}
	if o := loginOf([]byte(account("b@example.com", "t1"))); o.Binding == a.Binding {
		t.Error("another account kept the binding")
	}
	k := loginOf([]byte(`{"providers":{"meta":{"api_key":"mk-one"}}}`))
	if k.Route != routes.RouteMetaAPI || k.Binding == "" || strings.Contains(k.Binding, "mk-one") {
		t.Fatalf("api-key login = %+v", k)
	}
	if n := loginOf([]byte(`{"providers":{"meta":{"api_key":"mk-two"}}}`)); n.Binding == k.Binding {
		t.Error("a new key kept the binding")
	}
	for _, out := range []string{`{"schema_version":1,"providers":{}}`, `{`, ``} {
		if l := loginOf([]byte(out)); l != (routes.Launch{}) {
			t.Errorf("%q resolved to %+v", out, l)
		}
	}
	t.Setenv("META_API_KEY", "env-key")
	if l := Login(); l.Route != routes.RouteMetaAPI || l.Binding != routes.KeyBinding("META_API_KEY", "env-key") {
		t.Errorf("META_API_KEY in the environment = %+v", l)
	}
}
