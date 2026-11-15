package codex

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// codex signs in under a staging CODEX_HOME (agents.StagedPoll), never the live one: measured on
// 0.169.0, `codex login --device-auth` deletes the existing auth.json as soon as it starts.

// codexHome is the CODEX_HOME every session reads; the Agent never sets the variable.
func codexHome() string { return filepath.Join(paths.HomeDir(), ".codex") }

// newStagingHome makes a staging CODEX_HOME. The live config.toml is copied in so account-level
// settings (a forced login method or workspace) apply to the login as they would in place.
func newStagingHome() (string, error) {
	dir, err := agents.NewStagingHome("codex")
	if err != nil {
		return "", err
	}
	if b, err := os.ReadFile(filepath.Join(codexHome(), "config.toml")); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "config.toml"), b, 0o600)
	}
	return dir, nil
}

// loggedInAt is loggedIn for the CODEX_HOME at home. A var for tests.
var loggedInAt = func(home string) bool {
	cmd := exec.Command("codex", "login", "status")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return !strings.Contains(strings.ToLower(string(out)), "not logged in")
}

// installAuth installs the staged auth.json into home.
func installAuth(stage, home string) error {
	return agents.InstallFile(filepath.Join(stage, "auth.json"), filepath.Join(home, "auth.json"), 0o600)
}
