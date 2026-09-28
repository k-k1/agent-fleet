package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
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

// The thread runs in the session's CWD(), and trusting the working copy does not cover a
// chosen subdirectory (the trust dialog still appeared for one), so a launch into a
// subdirectory must pre-accept that folder as well — otherwise the TUI parks on the dialog.
func TestBuildLaunchTrustsTheSubdirItRunsIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sub := filepath.Join(dir, "pkg", "app")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Name: "trust-sub", Kind: session.KindCodex, Dir: dir, Subdir: "pkg/app"}
	plan, err := New().BuildLaunch(m, agents.LaunchOpts{})
	if err != nil {
		t.Fatalf("BuildLaunch: %v", err)
	}
	if plan.Cwd != sub {
		t.Fatalf("plan.Cwd = %q, want the subdirectory %q", plan.Cwd, sub)
	}
	got := readCodexConfig(t)
	for _, want := range []string{"[projects." + tomlString(dir) + "]", "[projects." + tomlString(sub) + "]"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
}

// codex matches trust against the resolved path, so a working copy reached through a symlink
// must have its target listed as well, or the TUI parks on the trust dialog anyway.
func TestBuildLaunchTrustsTheSymlinkTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	real := t.TempDir()
	if err := os.MkdirAll(filepath.Join(real, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	realResolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Name: "trust-link", Kind: session.KindCodex, Dir: link, Subdir: "sub"}
	if _, err := New().BuildLaunch(m, agents.LaunchOpts{}); err != nil {
		t.Fatalf("BuildLaunch: %v", err)
	}
	got := readCodexConfig(t)
	for _, d := range []string{link, filepath.Join(link, "sub"), realResolved, filepath.Join(realResolved, "sub")} {
		if want := "[projects." + tomlString(d) + "]"; !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
}
