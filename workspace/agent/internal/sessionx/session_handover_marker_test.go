package sessionx

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Only a directory the handover write created is marked as the Agent's own.
func TestWriteHandoverFileMarksOnlyWhatItCreated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := WriteHandoverFile("newone", "srcone", "md"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(paths.SessionWorkDir("newone"), paths.SessionWorkDirMarker)); err != nil {
		t.Fatalf("created dir not marked: %v", err)
	}
	own := paths.SessionWorkDir("mywork")
	if err := os.MkdirAll(own, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteHandoverFile("mywork", "srcone", "md"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(own, paths.SessionWorkDirMarker)); !os.IsNotExist(err) {
		t.Fatalf("existing dir was marked: %v", err)
	}
}
