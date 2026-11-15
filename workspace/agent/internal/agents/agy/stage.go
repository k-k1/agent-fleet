package agy

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// What an agy sign-in writes that matters (measured on a real sign-in under a scratch HOME): the
// token file alone. A home holding only it runs `agy models`; the onboarding state and
// ~/.config/antigravity/credential-user-scope it also writes are not needed, and the live home
// has finished onboarding already.

// tokenPathIn is tokenPath under home.
func tokenPathIn(home string) string {
	return filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// seedStagingSettings writes the staging home's settings.json before agy starts: loginDir is
// trusted (so no trust prompt) and telemetry is off, as the live settings are.
func seedStagingSettings(stage, loginDir string) error {
	b, err := json.MarshalIndent(map[string]any{
		"trustedWorkspaces": []string{loginDir},
		"enableTelemetry":   false,
	}, "", "  ")
	if err != nil {
		return err
	}
	return agents.WriteFileAtomic(filepath.Join(stage, ".gemini", "antigravity-cli", "settings.json"), append(b, '\n'), 0o600)
}
