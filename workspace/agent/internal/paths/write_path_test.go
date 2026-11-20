package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveFileWritePathRejectsMissingTraversal(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.toml")
	if err := os.Symlink("missing/../settings.toml", config); err != nil {
		t.Fatal(err)
	}
	if resolved, err := ResolveFileWritePath(config); err == nil {
		t.Fatalf("resolved %q across a missing directory", resolved)
	}
}
