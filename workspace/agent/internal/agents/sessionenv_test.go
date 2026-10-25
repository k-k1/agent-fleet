package agents

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

func TestWithSessionNameReplacesAnInheritedValue(t *testing.T) {
	base := []string{"PATH=/bin", "AF_SESSION_NAME=someone-else", "AF_SESSION_NAME_X=keep"}
	got := WithSessionName(base, "mine")
	want := []string{"PATH=/bin", "AF_SESSION_NAME_X=keep", "AF_SESSION_NAME=mine"}
	if !slices.Equal(got, want) {
		t.Errorf("WithSessionName = %v, want %v", got, want)
	}
	if base[1] != "AF_SESSION_NAME=someone-else" {
		t.Errorf("the caller's slice was modified: %v", base)
	}
	if got := WithSessionName(base, ""); slices.Contains(got, "AF_SESSION_NAME=someone-else") {
		t.Errorf("an unnamed child inherited another session's name: %v", got)
	}
}

func TestWithSessionEnvSetsThisSessionsWorkDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	base := []string{"PATH=/bin", "AF_WORK_DIR=/home/other/.af-work/someone-else", "AF_WORK_DIR_X=keep"}
	got := WithSessionEnv(base, "mine")
	wd := filepath.Join(home, ".af-work", "mine")
	if !slices.Contains(got, "AF_WORK_DIR="+wd) || !slices.Contains(got, "AF_SESSION_NAME=mine") {
		t.Fatalf("WithSessionEnv = %v, want AF_WORK_DIR=%s and AF_SESSION_NAME=mine", got, wd)
	}
	if slices.Contains(got, "AF_WORK_DIR=/home/other/.af-work/someone-else") || !slices.Contains(got, "AF_WORK_DIR_X=keep") {
		t.Errorf("inherited AF_WORK_DIR not replaced exactly: %v", got)
	}
	if fi, err := os.Stat(wd); err != nil || !fi.IsDir() {
		t.Fatalf("work dir not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wd, paths.SessionWorkDirMarker)); err != nil {
		t.Errorf("no ownership marker, so the boot prune would keep it for ever: %v", err)
	}
	if base[1] != "AF_WORK_DIR=/home/other/.af-work/someone-else" {
		t.Errorf("the caller's slice was modified: %v", base)
	}
}

// A name that could leave ~/.af-work gets no variable and creates nothing; the inherited value
// (another session's) is dropped all the same.
func TestWithSessionEnvRefusesAnEscapingName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, bad := range []string{"", ".", "..", "../x", "a/b"} {
		got := WithSessionEnv([]string{"AF_WORK_DIR=/elsewhere"}, bad)
		for _, kv := range got {
			if strings.HasPrefix(kv, "AF_WORK_DIR=") {
				t.Errorf("name %q: got %q", bad, kv)
			}
		}
	}
	if ents, _ := os.ReadDir(filepath.Join(home, ".af-work")); len(ents) != 0 {
		t.Errorf("a refused name created %v", ents)
	}
	if _, err := os.Stat(filepath.Join(home, "x")); err == nil {
		t.Error("an escaping name created a directory outside ~/.af-work")
	}
}

// Every managed kind that runs one vendor process per session builds that process's
// environment through WithSessionEnv. A kind that goes back to WithSessionName (or to
// os.Environ()) silently loses AF_WORK_DIR, which no behavioural test of the launch would notice
// without the vendor binary.
func TestPerSessionManagedKindsUseWithSessionEnv(t *testing.T) {
	for _, f := range []string{"copilot/driver.go", "cursor/driver.go", "kiro/driver.go", "muse/handle.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "agents.WithSessionEnv(") || strings.Contains(string(b), "agents.WithSessionName(") {
			t.Errorf("%s must build the child environment with agents.WithSessionEnv", f)
		}
	}
}
