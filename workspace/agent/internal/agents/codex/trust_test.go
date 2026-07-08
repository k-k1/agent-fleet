package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readCodexConfig(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	return string(b)
}

func TestEnsureCodexFolderTrustedCreates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/novel-idea"
	ensureFolderTrusted(dir)
	got := readCodexConfig(t)
	if !strings.Contains(got, `[projects."/home/dev/repos/novel-idea"]`) {
		t.Fatalf("missing project section:\n%s", got)
	}
	if !strings.Contains(got, `trust_level = "trusted"`) {
		t.Fatalf("missing trust_level:\n%s", got)
	}
}

func TestEnsureCodexFolderTrustedPreservesAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Pre-existing config with another trusted project and a top-level setting.
	existing := "model = \"gpt-5\"\n\n[projects.\"/home/dev/repos/codeleaf\"]\ntrust_level = \"trusted\"\n"
	cfg := filepath.Join(cfgDir, "config.toml")
	if err := os.WriteFile(cfg, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	dir := "/home/dev/repos/novel-idea"
	ensureFolderTrusted(dir)
	after := readCodexConfig(t)
	// Existing content preserved.
	if !strings.Contains(after, `model = "gpt-5"`) || !strings.Contains(after, `[projects."/home/dev/repos/codeleaf"]`) {
		t.Fatalf("clobbered existing config:\n%s", after)
	}
	// New section appended.
	if !strings.Contains(after, `[projects."/home/dev/repos/novel-idea"]`) {
		t.Fatalf("new section not appended:\n%s", after)
	}

	// Idempotent: a second call must not add a duplicate section.
	ensureFolderTrusted(dir)
	twice := readCodexConfig(t)
	if n := strings.Count(twice, `[projects."/home/dev/repos/novel-idea"]`); n != 1 {
		t.Fatalf("section duplicated %d times:\n%s", n, twice)
	}
}
