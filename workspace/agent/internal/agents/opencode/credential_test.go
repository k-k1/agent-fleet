package opencode

import (
	"slices"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

func TestResolveModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	keys := map[string]string{"ANTHROPIC_API_KEY": "sk-a", "OPENCODE_API_KEY": "oc-1", "MY_CUSTOM_KEY": "c"}
	noKey := map[string]string{}
	out := oauthState{known: true}
	unknown := oauthState{}
	acct := oauthState{known: true, connected: true, id: "cred-1"}
	engines := []string{"llamacpp"}
	zenKey := routes.Launch{Route: routes.RouteOpencodeZen, Binding: credbind.Key("OPENCODE_API_KEY", "oc-1")}
	for name, tc := range map[string]struct {
		model string
		keys  map[string]string
		pref  string
		acct  oauthState
		want  routes.Launch
	}{
		"provider key":     {"anthropic/claude-x", keys, UsageOwn, out, routes.Launch{Route: routes.RouteAnthropicAPI, Binding: credbind.Key("ANTHROPIC_API_KEY", "sk-a")}},
		"provider, no key": {"openai/gpt-x", keys, UsageOwn, out, routes.Launch{}},
		"zen key":          {"opencode/m", keys, UsageZen, out, zenKey},
		"go key":           {"opencode-go/m", keys, UsageGo, out, routes.Launch{Route: routes.RouteOpencodeGo, Binding: zenKey.Binding}},
		"account only":     {"opencode/m", noKey, UsageZen, acct, routes.Launch{Route: routes.RouteOpencodeAccount, Binding: credbind.Account(routes.RouteOpencodeAccount, "cred-1")}},
		"go, account only": {"opencode-go/m", noKey, UsageGo, acct, routes.Launch{Route: routes.RouteOpencodeAccount, Binding: credbind.Account(routes.RouteOpencodeAccount, "cred-1")}},
		// Both payers present: which one pays is not measured, and they are different classes.
		"key and account":    {"opencode/m", keys, UsageZen, acct, routes.Launch{}},
		"go, key and acct":   {"opencode-go/m", keys, UsageGo, acct, routes.Launch{}},
		"account unknown":    {"opencode/m", keys, UsageZen, unknown, routes.Launch{}},
		"no key, unknown":    {"opencode/m", noKey, UsageZen, unknown, routes.Launch{}},
		"signed in, no id":   {"opencode/m", noKey, UsageZen, oauthState{known: true, connected: true}, routes.Launch{}},
		"free, signed out":   {"opencode/m", keys, UsageFree, out, routes.Launch{Route: routes.RouteOpencodeZen}},
		"free, signed in":    {"opencode/m", keys, UsageFree, acct, routes.Launch{Route: routes.RouteOpencodeAccount, Binding: credbind.Account(routes.RouteOpencodeAccount, "cred-1")}},
		"go, nothing":        {"opencode-go/m", noKey, UsageGo, out, routes.Launch{}},
		"own keeps it out":   {"opencode/m", keys, UsageOwn, out, routes.Launch{}},
		"engine":             {"llamacpp/qwen", keys, UsageOwn, out, routes.Launch{Route: routes.RouteAFEngineLLM, Binding: credbind.Named(routes.RouteAFEngineLLM, "llm")}},
		"custom provider":    {"myprov/m", keys, UsageOwn, out, routes.Launch{Route: routes.RouteCustomAPI}},
		"bedrock, no select": {"amazon-bedrock/m", keys, UsageOwn, out, routes.Launch{}},
		"default model":      {"", keys, UsageOwn, out, routes.Launch{}},
		"billing off":        {"anthropic/claude-x", keys, UsageOff, out, routes.Launch{}},
	} {
		if got := resolveModel(tc.model, tc.keys, tc.pref, tc.acct, engines); got != tc.want {
			t.Errorf("%s: %+v, want %+v", name, got, tc.want)
		}
	}
	// Another sign-in is another credential id.
	other := oauthState{known: true, connected: true, id: "cred-2"}
	if a, b := resolveModel("opencode/m", nil, UsageZen, acct, nil), resolveModel("opencode/m", nil, UsageZen, other, nil); a.Binding == b.Binding {
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
