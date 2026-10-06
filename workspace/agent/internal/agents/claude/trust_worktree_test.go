package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func trustedIn(t *testing.T, dir string) (trusted, present bool) {
	t.Helper()
	b, err := os.ReadFile(claudeJSONPath())
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Projects map[string]map[string]any `json:"projects"`
	}
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	e, present := root.Projects[dir]
	trusted, _ = e["hasTrustDialogAccepted"].(bool)
	return trusted, present
}

// A linked worktree is trusted through its main checkout, which may carry an explicit false.
func TestEnsureFolderTrustedTrustsMainCheckoutOfWorktree(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	base := t.TempDir()
	main := filepath.Join(base, "repo")
	wt := filepath.Join(base, "repo@wip-x")
	for _, d := range []string{filepath.Join(main, ".git", "worktrees", "wip-x"), wt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitfile := "gitdir: " + filepath.Join(main, ".git", "worktrees", "wip-x") + "\n"
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte(gitfile), 0o644); err != nil {
		t.Fatal(err)
	}
	seed, _ := json.Marshal(map[string]any{"projects": map[string]any{main: map[string]any{"hasTrustDialogAccepted": false, "keep": 1}}})
	if err := os.WriteFile(claudeJSONPath(), seed, 0o600); err != nil {
		t.Fatal(err)
	}

	ensureFolderTrusted(wt)

	for _, d := range []string{wt, main} {
		if ok, _ := trustedIn(t, d); !ok {
			t.Errorf("%s not trusted", d)
		}
	}
}

// A plain checkout (.git is a directory) must not trust its parent directory.
func TestEnsureFolderTrustedPlainCheckoutTrustsOnlyItself(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	base := t.TempDir()
	dir := filepath.Join(base, "repo")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	ensureFolderTrusted(dir)
	if ok, _ := trustedIn(t, dir); !ok {
		t.Error("dir not trusted")
	}
	if _, present := trustedIn(t, base); present {
		t.Error("parent got an entry")
	}
}
