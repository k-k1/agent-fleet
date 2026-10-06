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
	b, _ := os.ReadFile(claudeJSONPath())
	var root struct {
		Projects map[string]map[string]any `json:"projects"`
	}
	_ = json.Unmarshal(b, &root)
	if root.Projects[main]["keep"] != float64(1) {
		t.Errorf("other keys of the main entry were dropped: %v", root.Projects[main])
	}
}

// Where the worktree is already trusted, only the main checkout's entry must be saved.
// gitdir forms: trailing "/" or "/.", relative.
func TestEnsureFolderTrustedMainOnlyAndGitdirForms(t *testing.T) {
	for name, suffix := range map[string]string{"plain": "", "slash": "/", "dot": "/.", "relative": "REL"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
			base := t.TempDir()
			main, wt := filepath.Join(base, "repo"), filepath.Join(base, "repo@wip-x")
			admin := filepath.Join(main, ".git", "worktrees", "wip-x")
			for _, d := range []string{admin, wt} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			g := admin + suffix
			if suffix == "REL" {
				g = "../repo/.git/worktrees/wip-x"
			}
			if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+g+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			seed, _ := json.Marshal(map[string]any{"hasCompletedOnboarding": true, "theme": "dark",
				"projects": map[string]any{wt: map[string]any{"hasTrustDialogAccepted": true}}})
			if err := os.WriteFile(claudeJSONPath(), seed, 0o600); err != nil {
				t.Fatal(err)
			}
			ensureFolderTrusted(wt)
			if ok, _ := trustedIn(t, main); !ok {
				t.Errorf("main checkout not saved as trusted")
			}
		})
	}
}

// Submodules and --separate-git-dir keep the common dir outside <checkout>/.git; the checkout
// is core.worktree. A separate dir without it must not trust the store's parent.
func TestMainCheckoutOfCoreWorktree(t *testing.T) {
	base := t.TempDir()
	checkout := filepath.Join(base, "outer", "sub")
	common := filepath.Join(base, "outer", ".git", "modules", "sub")
	wt := filepath.Join(base, "sub@wip-x")
	for _, d := range []string{filepath.Join(common, "worktrees", "wip-x"), checkout, wt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(common, "worktrees", "wip-x")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mainCheckoutOf(wt); len(got) != 0 {
		t.Errorf("no core.worktree and common not named .git: got %v", got)
	}
	cfg := "[core]\n\tworktree = ../../../sub\n"
	if err := os.WriteFile(filepath.Join(common, "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mainCheckoutOf(wt); len(got) != 1 || got[0] != checkout {
		t.Errorf("got %v, want [%s]", got, checkout)
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
