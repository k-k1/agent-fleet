package agy

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// What an agy sign-in writes that matters (measured on a real sign-in under a scratch HOME): the
// token file, which alone runs `agy models`, and on a first connection the onboarding state.
// ~/.config/antigravity/credential-user-scope is not needed.

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

// onboardingState is agy's record of the finished onboarding (color scheme, terms) under home.
func onboardingState(home string) string {
	return filepath.Join(home, ".gemini", "antigravity-cli", "jetski_state.pbtxt")
}

// installLogin installs the staged sign-in into home: the token, and the onboarding state when
// home has none. Without that state a first connection's sessions would open on the onboarding
// screens the staging run already answered; a home that has one keeps its own.
func installLogin(stage, home string) error {
	if err := agents.InstallFile(tokenPathIn(stage), tokenPathIn(home), 0o600); err != nil {
		return err
	}
	if fileExists(onboardingState(home)) || !fileExists(onboardingState(stage)) {
		return nil
	}
	return agents.InstallFile(onboardingState(stage), onboardingState(home), 0o600)
}
