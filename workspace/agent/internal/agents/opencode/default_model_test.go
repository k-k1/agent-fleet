package opencode

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// With no model named, the launch is resolved on the model opencode will choose: the config's
// "model" when the files agree, else the one provider every listed model is on; anything else is
// left unresolved rather than guessed.
func TestDefaultModel(t *testing.T) {
	dir := t.TempDir()
	file := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	global := file("global.json", `{"model":"anthropic/claude-x"}`)
	same := file("same.jsonc", "// a project file\n{\"model\": \"anthropic/claude-x\", /* c */ \"agent\": {}}")
	other := file("other.json", `{"model":"openai/gpt-x"}`)
	noModel := file("plain.json", `{"instructions":["a.md"]}`)
	broken := file("broken.json", `{"model": "openai/gpt-x",,`)
	brokenNoModel := file("broken2.json", `{"agent": ,`)
	missing := filepath.Join(dir, "missing.json")
	free := []string{"opencode/a-free", "opencode/b-free"}
	mixed := []string{"opencode/a-free", "anthropic/claude-x"}
	for name, tc := range map[string]struct {
		files, catalog []string
		want           string
	}{
		"config":                 {[]string{global, noModel, missing}, mixed, "anthropic/claude-x"},
		"two files agree":        {[]string{global, same}, mixed, "anthropic/claude-x"},
		"two files disagree":     {[]string{global, other}, free, ""},
		"unparseable with model": {[]string{global, broken}, free, ""},
		"unparseable, no model":  {[]string{brokenNoModel}, free, "opencode/a-free"},
		"one provider listed":    {[]string{noModel}, free, "opencode/a-free"},
		"providers differ":       {[]string{noModel}, mixed, ""},
		"no catalog":             {nil, nil, ""},
		"malformed catalog id":   {nil, []string{"opencode/a", "bare"}, ""},
	} {
		if got := defaultModel(tc.files, tc.catalog); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

// The live check's case: a session on opencode's default model, no key, signed out of the
// account, and only opencode.ai's free models listed. It launches on Zen with the keyless binding;
// with another provider listed beside them it stays unresolved (the control).
func TestResolveLaunchOnTheDefaultModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	oldView, oldCat := credentialView, catalogView
	t.Cleanup(func() { credentialView, catalogView = oldView, oldCat })
	credentialView = func() (map[string]string, string, oauthState) { return nil, UsageZen, oauthState{known: true} }
	catalog := []string{"opencode/a-free", "opencode/b-free"}
	catalogView = func() []string { return catalog }
	m := session.Meta{Name: "s", Kind: session.KindOpencode, Dir: t.TempDir()}
	want := routes.Launch{Route: routes.RouteOpencodeZen, Binding: FreeBinding}
	if got := (agentImpl{}).ResolveLaunch(m); got != want {
		t.Fatalf("default model, free only: %+v, want %+v", got, want)
	}
	if got := ModelLaunch(""); got != want {
		t.Errorf("chat on the default model: %+v, want %+v", got, want)
	}
	catalog = append(catalog, "anthropic/claude-x")
	if got := (agentImpl{}).ResolveLaunch(m); got != (routes.Launch{}) {
		t.Errorf("control: two providers listed should leave it unresolved: %+v", got)
	}
	// A project config naming the model decides, whatever is listed.
	if err := os.WriteFile(filepath.Join(m.Dir, "opencode.json"), []byte(`{"model":"opencode/a-free"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := (agentImpl{}).ResolveLaunch(m); got != want {
		t.Errorf("project config model: %+v, want %+v", got, want)
	}
	// A named model is the launch's own, config or not.
	m.Model = "anthropic/claude-x"
	credentialView = func() (map[string]string, string, oauthState) {
		return map[string]string{"ANTHROPIC_API_KEY": "sk-a"}, UsageZen, oauthState{known: true}
	}
	if got := (agentImpl{}).ResolveLaunch(m); got.Route != routes.RouteAnthropicAPI {
		t.Errorf("named model: %+v", got)
	}
}

// A session call is bound from the credentials that pay on the route it was filed under, and only
// when exactly one payer can be.
func TestCallBinding(t *testing.T) {
	key := map[string]string{"OPENCODE_API_KEY": "oc-1"}
	out := oauthState{known: true}
	acct := oauthState{known: true, connected: true, id: "cred-1"}
	for name, tc := range map[string]struct {
		route string
		keys  map[string]string
		pref  string
		acct  oauthState
		want  string
	}{
		"zen, keyless":         {routes.RouteOpencodeZen, nil, UsageZen, out, FreeBinding},
		"zen, key":             {routes.RouteOpencodeZen, key, UsageZen, out, credbind.Key("OPENCODE_API_KEY", "oc-1")},
		"go, key":              {routes.RouteOpencodeGo, key, UsageGo, out, credbind.Key("OPENCODE_API_KEY", "oc-1")},
		"go, nothing":          {routes.RouteOpencodeGo, nil, UsageGo, out, ""},
		"zen, account pays":    {routes.RouteOpencodeZen, nil, UsageZen, acct, ""},
		"zen, key and acct":    {routes.RouteOpencodeZen, key, UsageZen, acct, ""},
		"zen, account unknown": {routes.RouteOpencodeZen, nil, UsageZen, oauthState{}, ""},
		"billing off":          {routes.RouteOpencodeZen, nil, UsageOff, out, ""},
		"not a per-call route": {routes.RouteAnthropicAPI, map[string]string{"ANTHROPIC_API_KEY": "k"}, UsageOwn, out, ""},
	} {
		if got := callBinding(tc.route, tc.keys, tc.pref, tc.acct); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

// Only a catalog read under the current provider environment, not invalidated since and not older
// than catalogProofAge, proves a launch's provider; a launch never waits on a read under way.
func TestCatalogViewProvesOnlyACurrentCatalog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	modelsMu.Lock()
	saved := []any{modelsList, modelsAt, modelsEnvKey}
	modelsMu.Unlock()
	t.Cleanup(func() {
		modelsMu.Lock()
		modelsList, modelsAt, modelsEnvKey = saved[0].([]string), saved[1].(time.Time), saved[2].([sha256.Size]byte)
		modelsMu.Unlock()
	})
	current := sha256.Sum256([]byte(strings.Join(CurrentLaunch().Env(), "\x00")))
	set := func(at time.Time, key [sha256.Size]byte) {
		modelsMu.Lock()
		modelsList, modelsAt, modelsEnvKey = []string{"opencode/a-free"}, at, key
		modelsMu.Unlock()
	}
	set(time.Now(), current)
	if got := catalogView(); len(got) != 1 {
		t.Fatalf("a current catalog was not used: %v", got)
	}
	InvalidateModels()
	if got := catalogView(); got != nil {
		t.Errorf("an invalidated catalog proved a provider: %v", got)
	}
	set(time.Now(), sha256.Sum256([]byte("ANTHROPIC_API_KEY=old")))
	if got := catalogView(); got != nil {
		t.Errorf("a catalog read under another provider environment proved a provider: %v", got)
	}
	set(time.Now().Add(-catalogProofAge-time.Minute), current)
	if got := catalogView(); got != nil {
		t.Errorf("a stale catalog proved a provider: %v", got)
	}
	set(time.Now(), current)
	modelsMu.Lock()
	got := catalogView()
	modelsMu.Unlock()
	if got != nil {
		t.Errorf("a launch read the catalog while a read was under way: %v", got)
	}
}
