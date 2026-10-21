package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMakeSessionWorkDirMarksIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	wd, err := MakeSessionWorkDir("s1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wd, SessionWorkDirMarker)); err != nil {
		t.Fatalf("no marker: %v", err)
	}
	if _, err := MakeSessionWorkDir("../x"); err == nil {
		t.Fatal("escaping name accepted")
	}
}

// A member's own folder of the same name is not promoted to Agent-owned.
func TestMakeSessionWorkDirLeavesExistingUnmarked(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	wd := SessionWorkDir("mywork")
	if err := os.MkdirAll(wd, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := MakeSessionWorkDir("mywork"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wd, SessionWorkDirMarker)); !os.IsNotExist(err) {
		t.Fatalf("existing dir was marked: %v", err)
	}
}
