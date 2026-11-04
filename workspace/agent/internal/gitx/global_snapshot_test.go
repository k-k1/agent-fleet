package gitx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotAllowedRefusesIncludes(t *testing.T) {
	if !snapshotAllowed(nil, []byte("[user]\n\tname = x\n")) {
		t.Error("a plain config must be snapshotted")
	}
	for _, c := range []struct{ xdg, home string }{
		{"", "[include]\n\tpath = extra\n"},
		{"[IncludeIf \"gitdir:~/w/\"]\n\tpath = w\n", ""},
	} {
		if snapshotAllowed([]byte(c.xdg), []byte(c.home)) {
			t.Errorf("an include (%q / %q) must not be snapshotted", c.xdg, c.home)
		}
	}
}

func snapshotPath(t *testing.T, env []string) string {
	t.Helper()
	if len(env) != 1 || !strings.HasPrefix(env[0], "GIT_CONFIG_GLOBAL=") {
		t.Fatalf("env = %v", env)
	}
	return strings.TrimPrefix(env[0], "GIT_CONFIG_GLOBAL=")
}

func TestGlobalSnapshotGenerations(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	s := &globalSnapshot{}
	one := snapshotPath(t, s.env([]byte("[a]\n"), []byte("[b]\n")))
	root := filepath.Dir(filepath.Dir(one))
	if fi, err := os.Stat(root); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("snapshot dir must be private: %v %v", fi, err)
	}
	if again := snapshotPath(t, s.env([]byte("[a]\n"), []byte("[b]\n"))); again != one {
		t.Errorf("same content got a new generation: %s, want %s", again, one)
	}
	two := snapshotPath(t, s.env([]byte("[a]\n"), []byte("[c]\n")))
	if two == one {
		t.Fatal("changed content must get a new generation")
	}
	if b, _ := os.ReadFile(filepath.Join(filepath.Dir(two), "home")); string(b) != "[c]\n" {
		t.Errorf("new generation's home copy = %q", b)
	}
	if _, err := os.Stat(one); err != nil {
		t.Error("the previous generation must survive one change (a git may still read it)")
	}
	snapshotPath(t, s.env([]byte("[a]\n"), []byte("[d]\n")))
	if _, err := os.Stat(one); !os.IsNotExist(err) {
		t.Errorf("a generation two changes old must be removed: %v", err)
	}
}

// A snapshot directory that is no longer ours (cleaned away and recreated by someone else,
// or loosened) is never reused: its config could carry core.fsmonitor.
func TestGlobalSnapshotNeverReusesAForeignDir(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	s := &globalSnapshot{}
	first := snapshotPath(t, s.env(nil, []byte("[a]\n")))
	root := filepath.Dir(filepath.Dir(first))
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	// The same path recreated with a planted generation, as a different owner would.
	if err := os.MkdirAll(filepath.Dir(first), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, first, "[core]\n\tfsmonitor = /planted\n")
	got := snapshotPath(t, s.env(nil, []byte("[a]\n")))
	if strings.HasPrefix(got, root+string(filepath.Separator)) {
		t.Fatalf("reused the replaced directory: %s", got)
	}
	if b, _ := os.ReadFile(got); strings.Contains(string(b), "planted") {
		t.Fatalf("handed out the planted config: %q", b)
	}
}

func isolateGlobalConfig(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	os.Unsetenv("GIT_CONFIG_GLOBAL")
	saved := listGlobalSnapshot
	listGlobalSnapshot = &globalSnapshot{}
	t.Cleanup(func() { listGlobalSnapshot = saved })
	return home
}

func untrackedRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "copy")
	gitInit(t, dir)
	writeFile(t, filepath.Join(dir, "untracked.txt"), "u\n")
	return dir
}

func newTestCache() *repoCache {
	return &repoCache{entries: map[string]*copyEntry{}, sem: make(chan struct{}, 1), max: maxCacheEntries}
}

// The list poll's git status reads the global config from the snapshot, not from the home
// files: with ~/.gitconfig gone after the snapshot was taken, its setting still applies.
func TestListStatusReadsGlobalConfigFromSnapshot(t *testing.T) {
	home := isolateGlobalConfig(t)
	dir := untrackedRepo(t)
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
	if st, _ := newTestCache().inspect(dir, g); st.Untracked != 0 {
		t.Errorf("list cache untracked = %d: its git status did not get the snapshot", st.Untracked)
	}
	if st, _ := GitStatus(dir); st.Untracked != 1 {
		t.Errorf("plain GitStatus untracked = %d, want 1 (it must keep reading the real files)", st.Untracked)
	}

	writeFile(t, filepath.Join(home, ".gitconfig"), "[include]\n\tpath = more\n")
	if g := globalConfigPaths(); g.statusEnv != nil {
		t.Errorf("a config with an include got a snapshot: %v", g.statusEnv)
	}
	writeFile(t, filepath.Join(home, ".gitconfig"), "[status]\n\tshowUntrackedFiles = no\n")
	for _, v := range []string{"", filepath.Join(home, "other")} {
		t.Setenv("GIT_CONFIG_GLOBAL", v)
		if g := globalConfigPaths(); g.statusEnv != nil {
			t.Errorf("GIT_CONFIG_GLOBAL=%q already set, yet a snapshot was made: %v", v, g.statusEnv)
		}
	}
}

// File boundaries survive the snapshot: a BOM opening ~/.gitconfig and a trailing backslash
// in the XDG file both read the way git reads the two real files.
func TestSnapshotKeepsFileBoundaries(t *testing.T) {
	for name, c := range map[string]struct{ xdg, home string }{
		"bom":          {"[x]\n\ty = 1\n", "\xef\xbb\xbf[status]\n\tshowUntrackedFiles = no\n"},
		"continuation": {"[core]\n\tname = test\\\n", "[status]\n\tshowUntrackedFiles = no\n"},
	} {
		t.Run(name, func(t *testing.T) {
			home := isolateGlobalConfig(t)
			dir := untrackedRepo(t)
			writeFile(t, filepath.Join(home, ".config", "git", "config"), c.xdg)
			writeFile(t, filepath.Join(home, ".gitconfig"), c.home)
			want, err := GitStatus(dir)
			if err != nil {
				t.Fatalf("git on the real files: %v", err)
			}
			g := globalConfigPaths()
			if g.statusEnv == nil {
				t.Fatal("no snapshot")
			}
			got, err := gitStatusEnv(dir, g.statusEnv)
			if err != nil || got != want {
				t.Errorf("through the snapshot: %+v err=%v, want %+v", got, err, want)
			}
		})
	}
}

// An edit between this request's read of the global config and its status run must not be
// cached under the new config's fingerprint.
func TestSnapshotStatusNotCachedAcrossAnEdit(t *testing.T) {
	home := isolateGlobalConfig(t)
	dir := untrackedRepo(t)
	cfg := filepath.Join(home, ".gitconfig")
	writeFile(t, cfg, "[status]\n\tshowUntrackedFiles = no\n")
	c := newTestCache()
	g := globalConfigPaths()
	writeFile(t, cfg, "[status]\n\tshowUntrackedFiles = normal\n")
	c.inspect(dir, g) // answers from the old snapshot
	if st, _ := c.inspect(dir, globalConfigPaths()); st.Untracked != 1 {
		t.Errorf("after the edit the list says untracked = %d, want 1", st.Untracked)
	}
}
