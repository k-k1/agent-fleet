package opencode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// A launch on opencode's own default model stays unresolved even when a config names a model:
// opencode falls back from a configured model that is unavailable or disabled, and an agent's
// own model overrides it, so the config does not prove which model will run. A named model is
// resolved as before, the keyless free route with its fixed binding.
func TestDefaultModelLaunchStaysUnresolved(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	oldView := credentialView
	t.Cleanup(func() { credentialView = oldView })
	credentialView = func() (map[string]string, string, oauthState, *secrets.CustomAPIConn) {
		return nil, UsageZen, oauthState{known: true}, nil
	}
	m := session.Meta{Name: "s", Kind: session.KindOpencode, Dir: t.TempDir()}
	cfg := []byte(`{"model":"opencode/a-free"}`)
	if err := os.WriteFile(filepath.Join(m.Dir, "opencode.json"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".config", "opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "opencode", "opencode.json"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := (agentImpl{}).ResolveLaunch(m); got != (routes.Launch{}) {
		t.Errorf("a configured default resolved the launch: %+v", got)
	}
	if got := ModelLaunch(""); got != (routes.Launch{}) {
		t.Errorf("a configured default resolved the chat: %+v", got)
	}
	m.Model = "opencode/a-free"
	if got, want := (agentImpl{}).ResolveLaunch(m), (routes.Launch{Route: routes.RouteOpencodeZen, Binding: FreeBinding}); got != want {
		t.Errorf("a named free model: %+v, want %+v", got, want)
	}
}
