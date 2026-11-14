package codex

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

func jwt(payload string) string {
	return "h." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".s"
}

// Both login modes resolve to their own route; a token refresh keeps the binding, another
// account or another key changes it (ADR 0138 M0 gate).
func TestLoginOf(t *testing.T) {
	chatgpt := func(acct, access string) string {
		return `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"` + jwt(`{"email":"a@example.com"}`) +
			`","access_token":"` + access + `","refresh_token":"r-` + access + `","account_id":"` + acct + `"}}`
	}
	a := loginOf([]byte(chatgpt("acct-1", "t1")))
	if a.Route != routes.RouteChatGPTSubscription || a.Binding == "" || strings.Contains(a.Binding, "acct-1") {
		t.Fatalf("chatgpt login = %+v", a)
	}
	if r := loginOf([]byte(chatgpt("acct-1", "t2"))); r != a {
		t.Errorf("a token refresh changed the launch: %+v vs %+v", r, a)
	}
	if o := loginOf([]byte(chatgpt("acct-2", "t1"))); o.Binding == a.Binding {
		t.Error("another account kept the binding")
	}
	// No account_id: the id_token's claim names the account.
	noID := `{"auth_mode":"chatgpt","tokens":{"id_token":"` + jwt(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct-1"}}`) + `"}}`
	if r := loginOf([]byte(noID)); r.Route != routes.RouteChatGPTSubscription || r.Binding != a.Binding {
		t.Errorf("id_token account = %+v, want binding %q", r, a.Binding)
	}

	k := loginOf([]byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-one"}`))
	if k.Route != routes.RouteOpenAIAPI || k.Binding != routes.KeyBinding(apiKeySlot, "sk-one") {
		t.Fatalf("api-key login = %+v", k)
	}
	if n := loginOf([]byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-two"}`)); n.Binding == k.Binding {
		t.Error("a new key kept the binding")
	}
	if l := loginOf([]byte(`{"OPENAI_API_KEY":"sk-one"}`)); l != k {
		t.Errorf("a file without auth_mode = %+v, want %+v", l, k)
	}
	for _, bad := range []string{``, `{`, `{}`, `{"auth_mode":"other"}`} {
		if l := loginOf([]byte(bad)); l != (routes.Launch{}) {
			t.Errorf("%q resolved to %+v", bad, l)
		}
	}
}
