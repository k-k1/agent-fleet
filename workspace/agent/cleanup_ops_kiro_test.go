package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Deleting a session drops its kiro ACP recorder file (it holds the whole ACP traffic).
func TestRemoveSessionSideFilesDropsKiroRecorder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := filepath.Join(paths.AgentStateDir(), "kiro-acp", "sdel1.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	removeSessionSideFiles("sdel1")
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("recorder file survived session deletion, stat err=%v", err)
	}
}
