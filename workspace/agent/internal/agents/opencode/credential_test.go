package opencode

import (
	"slices"
	"testing"
	"time"

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

// The launch reads the account view only while it is current: after a login or a disconnect
// invalidates it, or once it is older than its TTL, it is unknown (so no payer is proven) and a
// background refresh is started, never waited for.
func TestOAuthCachedIsCurrentOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	refreshes := 0
	oldRefresh := refreshOAuthAsync
	refreshOAuthAsync = func() { refreshes++ }
	oauthMu.Lock()
	oldCache, oldAt := oauthCache, oauthAt
	oauthMu.Unlock()
	t.Cleanup(func() {
		refreshOAuthAsync = oldRefresh
		oauthMu.Lock()
		oauthCache, oauthAt = oldCache, oldAt
		oauthMu.Unlock()
	})
	seed := func(st oauthState, at time.Time) {
		oauthMu.Lock()
		oauthCache, oauthAt = st, at
		oauthMu.Unlock()
	}
	resolve := func() routes.Launch { return resolveModel("opencode/m", nil, UsageZen, oauthCached(), nil) }
	accountA := routes.Launch{Route: routes.RouteOpencodeAccount, Binding: credbind.Account(routes.RouteOpencodeAccount, "cred-A")}

	seed(oauthState{known: true, connected: true, id: "cred-A"}, time.Now())
	if got := resolve(); got != accountA {
		t.Fatalf("current account A = %+v", got)
	}
	// Account A -> B: the disconnect/login invalidates; the next create must not record A.
	invalidateOAuthStatus()
	if got := resolve(); got != (routes.Launch{}) {
		t.Errorf("after invalidation: %+v, want unknown", got)
	}
	if refreshes == 0 {
		t.Error("an invalidated view started no background refresh")
	}
	seed(oauthState{known: true, connected: true, id: "cred-B"}, time.Now())
	if got := resolve(); got.Binding == accountA.Binding || got.Route != routes.RouteOpencodeAccount {
		t.Errorf("account B = %+v", got)
	}
	// Signed out -> signed in: an old signed-out view must not prove "only the key pays".
	seed(oauthState{known: true}, time.Now().Add(-2*oauthStatusTTL))
	if got := resolveModel("opencode/m", map[string]string{opencodeKeyEnv: "k"}, UsageZen, oauthCached(), nil); got != (routes.Launch{}) {
		t.Errorf("expired signed-out view with a key: %+v, want unknown", got)
	}
	// A refresh in flight holds the lock over the daemon call: unknown, not a wait.
	seed(oauthState{known: true, connected: true, id: "cred-A"}, time.Now())
	oauthMu.Lock()
	start := time.Now()
	st := oauthCached()
	oauthMu.Unlock()
	if st.known || time.Since(start) > time.Second {
		t.Errorf("during a refresh: %+v after %v", st, time.Since(start))
	}
}
