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
