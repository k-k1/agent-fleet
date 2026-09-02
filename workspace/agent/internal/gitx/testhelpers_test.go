package gitx

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile は移送元（package main の repo_prompts_test.go）にあるヘルパの写し。
// git_submodule_sync_test.go だけが使っており、中身は MkdirAll + WriteFile の
// 素の 2 行なので、配線するより写す方が読める。
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
