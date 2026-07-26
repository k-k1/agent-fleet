//go:build tui_contract

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// TestCursorTUIMirrorContract verifies the production terminal route. Cursor's
// TUI writes the JSONL that /messages reads, unlike its managed ACP route whose
// mirror is in-memory, so this specifically protects the Terminal (CLI) contract.
func TestCursorTUIMirrorContract(t *testing.T) {
	if _, err := exec.LookPath(cursor.Bin()); err != nil {
		requireTUIContract(t, false, "cursor-agent が PATH にありません: "+err.Error())
	}
	requireTUIContract(t, cursor.LoggedIn(), "Cursor が認証済みではありません（E2E_CURSOR_AUTH_JSON を設定してください）")

	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	authDir := filepath.Join(realHome, ".config", "cursor")
	if _, err := os.Stat(authDir); err != nil {
		requireTUIContract(t, false, "Cursor 認証ディレクトリがありません: "+err.Error())
	}
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(authDir, filepath.Join(home, ".config", "cursor")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	runTUIMirrorContract(t, tuiMirrorContractSpec{kind: session.KindCursor, agent: cursor.New()})
}
