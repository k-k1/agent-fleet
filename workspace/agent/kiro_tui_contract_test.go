//go:build tui_contract

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// TestKiroTUIMirrorContract drives the production BuildLaunch path instead of the
// package-local probe. This includes the launch readiness gate, post-launch SID
// discovery, v2 JSONL mirror parsing, and the pane-text working→idle state source.
func TestKiroTUIMirrorContract(t *testing.T) {
	if _, err := exec.LookPath(kiro.Bin()); err != nil {
		requireTUIContract(t, false, "kiro-cli が PATH にありません: "+err.Error())
	}
	requireTUIContract(t, kiro.LoggedIn(), "Kiro が認証済みではありません（E2E_KIRO_AUTH_DB_B64 を設定してください）")

	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	for _, rel := range []string{".local/share/kiro-cli", ".kiro/settings"} {
		src := filepath.Join(realHome, rel)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dst := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(src, dst); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)

	runTUIMirrorContract(t, tuiMirrorContractSpec{kind: session.KindKiro, agent: kiro.New()})
}
