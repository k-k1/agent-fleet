package memengine

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Agent compiles from workspace/ alone (the image's build context), so the engine module in
// ../../../../memengine cannot be a `replace` target. This directory is that module's non-test
// sources, copied by scripts/sync-memengine.sh; the test makes a drifting copy a red build. Where
// the engine directory is not there (a build context of workspace/ only) there is nothing to
// compare with and the test skips.
func TestMirrorMatchesEngine(t *testing.T) {
	src := filepath.Join("..", "..", "..", "..", "memengine")
	ents, err := os.ReadDir(src)
	if err != nil {
		t.Skipf("engine sources not present: %v", err)
	}
	seen := 0
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		seen++
		want, err := os.ReadFile(filepath.Join(src, n))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(n)
		if err != nil {
			t.Errorf("%s is in memengine/ but not in the mirror: run scripts/sync-memengine.sh", n)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from memengine/%s: run scripts/sync-memengine.sh", n, n)
		}
	}
	if seen == 0 {
		t.Fatal("no engine sources found: the comparison compared nothing")
	}
	// The other direction: a file only the mirror has.
	mine, _ := os.ReadDir(".")
	for _, e := range mine {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		if _, err := os.Stat(filepath.Join(src, n)); err != nil {
			t.Errorf("%s is only in the mirror: remove it or run scripts/sync-memengine.sh", n)
		}
	}
}
