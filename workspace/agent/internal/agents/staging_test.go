package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A CLI must derive every path from the staging home: a leftover XDG_* would send its writes to
// the live home.
func TestWithHomeReplacesHomeAndDropsXDG(t *testing.T) {
	got := WithHome([]string{"PATH=/bin", "HOME=/live", "XDG_CONFIG_HOME=/live/.config", "XDG_DATA_HOME=/x", "TERM=xterm"}, "/stage")
	want := "PATH=/bin TERM=xterm HOME=/stage"
	if strings.Join(got, " ") != want {
		t.Fatalf("WithHome = %q, want %q", strings.Join(got, " "), want)
	}
}

func TestInstallFileReplacesWithMode(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "sub", "dst")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InstallFile(src, dst, 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dst)
	fi, _ := os.Stat(dst)
	if string(b) != "new" || fi.Mode().Perm() != 0o600 {
		t.Fatalf("dst = %q %v", b, fi.Mode())
	}
	if ents, _ := os.ReadDir(filepath.Dir(dst)); len(ents) != 1 {
		t.Fatalf("temporary files left: %v", ents)
	}
	if err := InstallFile(filepath.Join(dir, "absent"), dst, 0o600); err == nil {
		t.Fatal("installed a file that does not exist")
	}
	if b, _ := os.ReadFile(dst); string(b) != "new" {
		t.Fatalf("a failed install touched dst: %q", b)
	}
}

// Homes left by a dead Agent are swept; one in use (younger than the TTL) is not.
func TestNewStagingHomeSweepsOnlyStaleHomes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stale, err := NewStagingHome("x")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * stagingTTL)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	fresh, err := NewStagingHome("x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewStagingHome("y"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale home kept: %v", err)
	}
	if fi, err := os.Stat(fresh); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("fresh home: %v %v", fi, err)
	}
}
