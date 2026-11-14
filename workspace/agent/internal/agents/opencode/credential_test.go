package opencode

import (
	"slices"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

func TestResolveModel(t *testing.T) {
	keys := map[string]string{"ANTHROPIC_API_KEY": "sk-a", "OPENCODE_API_KEY": "oc-1", "MY_CUSTOM_KEY": "c"}
	none := oauthState{}
	acct := oauthState{connected: true, id: "cred-1"}
	engines := []string{"llamacpp"}
	for name, tc := range map[string]struct {
		model string
		keys  map[string]string
		pref  string
		acct  oauthState
		want  routes.Launch
	}{
		"provider key":       {"anthropic/claude-x", keys, UsageOwn, none, routes.Launch{Route: routes.RouteAnthropicAPI, Binding: routes.KeyBinding("ANTHROPIC_API_KEY", "sk-a")}},
		"provider, no key":   {"openai/gpt-x", keys, UsageOwn, none, routes.Launch{}},
		"zen key":            {"opencode/m", keys, UsageZen, none, routes.Launch{Route: routes.RouteOpencodeZen, Binding: routes.KeyBinding("OPENCODE_API_KEY", "oc-1")}},
		"go key":             {"opencode-go/m", keys, UsageGo, none, routes.Launch{Route: routes.RouteOpencodeGo, Binding: routes.KeyBinding("OPENCODE_API_KEY", "oc-1")}},
		"account only":       {"opencode/m", map[string]string{}, UsageZen, acct, routes.Launch{Route: routes.RouteOpencodeAccount, Binding: routes.AccountBinding(routes.RouteOpencodeAccount, "cred-1")}},
		"key and account":    {"opencode/m", keys, UsageZen, acct, routes.Launch{Route: routes.RouteOpencodeZen}},
		"free tier":          {"opencode/m", keys, UsageFree, acct, routes.Launch{Route: routes.RouteOpencodeZen}},
		"engine":             {"llamacpp/qwen", keys, UsageOwn, none, routes.Launch{Route: routes.RouteAFEngineLLM, Binding: "af-engine-llm@llm"}},
		"custom provider":    {"myprov/m", keys, UsageOwn, none, routes.Launch{Route: routes.RouteCustomAPI}},
		"bedrock, no select": {"amazon-bedrock/m", keys, UsageOwn, none, routes.Launch{}},
		"default model":      {"", keys, UsageOwn, none, routes.Launch{}},
		"billing off":        {"anthropic/claude-x", keys, UsageOff, none, routes.Launch{}},
	} {
		if got := resolveModel(tc.model, tc.keys, tc.pref, tc.acct, engines); got != tc.want {
			t.Errorf("%s: %+v, want %+v", name, got, tc.want)
		}
	}
	// Another sign-in is another credential id; a new key another fingerprint.
	if a, b := resolveModel("opencode/m", nil, UsageZen, acct, nil), resolveModel("opencode/m", nil, UsageZen, oauthState{connected: true, id: "cred-2"}, nil); a.Binding == b.Binding {
		t.Error("another account kept the binding")
	}
}

func TestUsableOf(t *testing.T) {
	keys := map[string]string{"ANTHROPIC_API_KEY": "sk-a", "OPENCODE_API_KEY": "oc-1", "MY_CUSTOM_KEY": "c", "EMPTY": ""}
	got := usableOf(keys, UsageZen, oauthState{connected: true}, true)
	want := []string{routes.RouteAFEngineLLM, routes.RouteAnthropicAPI, routes.RouteCustomAPI, routes.RouteOpencodeAccount, routes.RouteOpencodeGo, routes.RouteOpencodeZen}
	if !slices.Equal(got, want) {
		t.Errorf("zen: %v, want %v", got, want)
	}
	if got := usableOf(keys, UsageOwn, oauthState{connected: true}, false); !slices.Equal(got, []string{routes.RouteAnthropicAPI, routes.RouteCustomAPI}) {
		t.Errorf("own: %v", got)
	}
	if got := usableOf(keys, UsageOff, oauthState{connected: true}, true); got != nil {
		t.Errorf("off: %v", got)
	}
}
