//go:build tui_contract

package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// TestCopilotTUIMirrorContract is the interactive counterpart of copilot's ACP
// live test. It proves the Terminal (CLI) route reaches the composer, writes the
// real events.jsonl mirror source, and returns its production LiveState to idle.
func TestCopilotTUIMirrorContract(t *testing.T) {
	if _, err := exec.LookPath("copilot"); err != nil {
		requireTUIContract(t, false, "copilot が PATH にありません: "+err.Error())
	}
	// Resolve before HOME isolation: gh transparent auth is the production source.
	// The token is passed only as an environment variable to the child, never a CLI
	// argument or test log.
	token := copilot.Token()
	requireTUIContract(t, token != "", "Copilot の GitHub OAuth token がありません（E2E_COPILOT_GITHUB_TOKEN を設定してください）")

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("COPILOT_HOME", filepath.Join(home, ".copilot"))
	t.Setenv("COPILOT_GITHUB_TOKEN", token)

	runTUIMirrorContract(t, tuiMirrorContractSpec{kind: session.KindCopilot, agent: copilot.New()})
}
