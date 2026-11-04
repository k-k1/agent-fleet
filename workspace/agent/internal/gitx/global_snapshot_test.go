package gitx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotContentOrderAndIncludes(t *testing.T) {
	got, ok := snapshotContent([]byte("[a]\n\tx = xdg"), []byte("[a]\n\tx = home\n"))
	if !ok || string(got) != "[a]\n\tx = xdg\n[a]\n\tx = home\n" {
		t.Fatalf("content = %q ok=%v; want the XDG file first, then ~/.gitconfig", got, ok)
	}
	if got, ok := snapshotContent(nil, nil); !ok || len(got) != 0 {
		t.Fatalf("no config: content = %q ok=%v, want empty and ok", got, ok)
	}
	for _, c := range []struct{ xdg, home string }{
		{"", "[include]\n\tpath = extra\n"},
		{"[IncludeIf \"gitdir:~/w/\"]\n\tpath = w\n", ""},
	} {
		if _, ok := snapshotContent([]byte(c.xdg), []byte(c.home)); ok {
			t.Errorf("an include (%q / %q) must not be snapshotted", c.xdg, c.home)
		}
	}
}

func TestGlobalSnapshotWritesOnlyOnChange(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	s := &globalSnapshot{}
	env := s.env([]byte("one"))
	if len(env) != 1 || !strings.HasPrefix(env[0], "GIT_CONFIG_GLOBAL=") {
		t.Fatalf("env = %v", env)
	}
	path := strings.TrimPrefix(env[0], "GIT_CONFIG_GLOBAL=")
	if fi, err := os.Stat(filepath.Dir(path)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("snapshot dir must be private: %v %v", fi, err)
	}
	before, _ := os.Stat(path)
	s.env([]byte("one"))
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Error("unchanged content must not rewrite the snapshot")
	}
	s.env([]byte("two"))
	if b, _ := os.ReadFile(path); string(b) != "two" {
		t.Errorf("changed content: snapshot holds %q, want %q", b, "two")
	}
	// A cleaned-away temp dir: that call falls back to the real files, the next makes a new one.
	if err := os.RemoveAll(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if env := s.env([]byte("two")); env != nil {
		t.Errorf("env with its directory gone = %v, want nil (read the real files)", env)
	}
	env = s.env([]byte("two"))
	if env == nil {
		t.Fatal("the next call must make a new snapshot directory")
	}
	if b, _ := os.ReadFile(strings.TrimPrefix(env[0], "GIT_CONFIG_GLOBAL=")); string(b) != "two" {
		t.Errorf("new snapshot holds %q", b)
	}
}

// The list poll's git status reads the global config from the snapshot, not from the home
// files: with ~/.gitconfig gone after the snapshot was taken, its setting still applies.
func TestListStatusReadsGlobalConfigFromSnapshot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	os.Unsetenv("GIT_CONFIG_GLOBAL")
	saved := listGlobalSnapshot
	listGlobalSnapshot = &globalSnapshot{}
	t.Cleanup(func() { listGlobalSnapshot = saved })
	dir := filepath.Join(t.TempDir(), "copy")
	gitInit(t, dir)
	writeFile(t, filepath.Join(dir, "untracked.txt"), "u\n")
	writeFile(t, filepath.Join(home, ".gitconfig"), "[status]\n\tshowUntrackedFiles = no\n")

	g := globalConfigPaths()
	if g.statusEnv == nil {
		t.Fatal("no snapshot for a plain ~/.gitconfig")
	}
	if err := os.Remove(filepath.Join(home, ".gitconfig")); err != nil {
		t.Fatal(err)
	}
	st, err := gitStatusEnv(dir, g.statusEnv)
	if err != nil {
		t.Fatal(err)
	}
	if st.Untracked != 0 {
		t.Errorf("untracked = %d: git did not read the snapshot's status.showUntrackedFiles=no", st.Untracked)
	}
	c := &repoCache{entries: map[string]*copyEntry{}, sem: make(chan struct{}, 1), max: maxCacheEntries}
	if st, _ := c.inspect(dir, g); st.Untracked != 0 {
		t.Errorf("list cache untracked = %d: its git status did not get the snapshot", st.Untracked)
	}
	if st, _ := GitStatus(dir); st.Untracked != 1 {
		t.Errorf("plain GitStatus untracked = %d, want 1 (it must keep reading the real files)", st.Untracked)
	}

	writeFile(t, filepath.Join(home, ".gitconfig"), "[include]\n\tpath = more\n")
	if g := globalConfigPaths(); g.statusEnv != nil {
		t.Errorf("a config with an include got a snapshot: %v", g.statusEnv)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "other"))
	if g := globalConfigPaths(); g.statusEnv != nil {
		t.Errorf("GIT_CONFIG_GLOBAL already set, yet a snapshot was made: %v", g.statusEnv)
	}
}
