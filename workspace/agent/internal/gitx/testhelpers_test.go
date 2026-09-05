package gitx

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile is a copy of the helper in package main's repo_prompts_test.go. Only
// git_submodule_sync_test.go uses it, and the body is a bare MkdirAll + WriteFile, so copying
// reads better than wiring the two together.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
