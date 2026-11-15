package opencode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// With no model named, the launch is resolved on the config's "model" when the files that set one
// agree, and on nothing else: an agent's own model, a disagreement or an unreadable file that
// mentions a model leaves it unresolved rather than guessed.
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
	same := file("same.jsonc", "// a project file\n{\"model\": \"anthropic/claude-x\", /* c */ \"permission\": {}}")
	other := file("other.json", `{"model":"openai/gpt-x"}`)
	noModel := file("plain.json", `{"instructions":["a.md"],"agent":{"plan":{"prompt":"p"}}}`)
	agentModel := file("agent.json", `{"agent":{"build":{"model":"openai/gpt-x"}}}`)
	modeModel := file("mode.json", `{"mode":{"build":{"model":"openai/gpt-x"}}}`)
	broken := file("broken.json", `{"model": "openai/gpt-x",,`)
	brokenNoModel := file("broken2.json", `{"agent": ,`)
	missing := filepath.Join(dir, "missing.json")
	for name, tc := range map[string]struct {
		files []string
		want  string
	}{
		"config":                 {[]string{global, noModel, missing}, "anthropic/claude-x"},
		"two files agree":        {[]string{global, same}, "anthropic/claude-x"},
		"two files disagree":     {[]string{global, other}, ""},
		"an agent's own model":   {[]string{global, agentModel}, ""},
		"a mode's own model":     {[]string{global, modeModel}, ""},
		"unparseable with model": {[]string{global, broken}, ""},
		"unparseable, no model":  {[]string{global, brokenNoModel}, "anthropic/claude-x"},
		"no model configured":    {[]string{noModel, missing}, ""},
		"no files":               {nil, ""},
	} {
		if got := defaultModel(tc.files); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

// A session on the default model resolves through the config that applies to its own project:
// the project's "model" decides, and the keyless free route carries its fixed binding. Without a
// configured model it stays unresolved, whatever opencode might list.
func TestResolveLaunchOnTheDefaultModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	oldView := credentialView
	t.Cleanup(func() { credentialView = oldView })
	credentialView = func() (map[string]string, string, oauthState) { return nil, UsageZen, oauthState{known: true} }
	m := session.Meta{Name: "s", Kind: session.KindOpencode, Dir: t.TempDir()}
	if got := (agentImpl{}).ResolveLaunch(m); got != (routes.Launch{}) {
		t.Fatalf("no configured model must stay unresolved: %+v", got)
	}
	if err := os.WriteFile(filepath.Join(m.Dir, "opencode.json"), []byte(`{"model":"opencode/a-free"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	want := routes.Launch{Route: routes.RouteOpencodeZen, Binding: FreeBinding}
	if got := (agentImpl{}).ResolveLaunch(m); got != want {
		t.Errorf("project config model: %+v, want %+v", got, want)
	}
	// Another project's config is not this launch's.
	other := session.Meta{Name: "o", Kind: session.KindOpencode, Dir: t.TempDir()}
	if got := (agentImpl{}).ResolveLaunch(other); got != (routes.Launch{}) {
		t.Errorf("another project's config resolved this launch: %+v", got)
	}
	// The global config applies to every launch, the assistant chat's included.
	if err := os.MkdirAll(filepath.Join(home, ".config", "opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "opencode", "opencode.json"), []byte(`{"model":"opencode/a-free"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ModelLaunch(""); got != want {
		t.Errorf("chat on the global config's model: %+v, want %+v", got, want)
	}
	// The chat's working directory is under the home directory, whose own opencode.json applies
	// to it too: two models configured leave it unresolved.
	if err := os.WriteFile(filepath.Join(home, "opencode.json"), []byte(`{"model":"openai/gpt-x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ModelLaunch(""); got != (routes.Launch{}) {
		t.Errorf("the chat ignored the home directory's config: %+v", got)
	}
	_ = os.Remove(filepath.Join(home, "opencode.json"))
	// A named model is the launch's own, config or not.
	m.Model = "anthropic/claude-x"
	credentialView = func() (map[string]string, string, oauthState) {
		return map[string]string{"ANTHROPIC_API_KEY": "sk-a"}, UsageZen, oauthState{known: true}
	}
	if got := (agentImpl{}).ResolveLaunch(m); got.Route != routes.RouteAnthropicAPI {
		t.Errorf("named model: %+v", got)
	}
}
