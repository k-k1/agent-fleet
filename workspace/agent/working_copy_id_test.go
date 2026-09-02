package main

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkingCopyIDStableAndDoesNotResurrectAfterRecreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	first := gitx.WorkingCopyID(dir)
	if first == "" || first != gitx.WorkingCopyID(dir) {
		t.Fatalf("working copy id is not stable: %q", first)
	}
	if err := os.WriteFile(filepath.Join(dir, "new-file"), []byte("change directory ctime"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := gitx.WorkingCopyID(dir); got != first {
		t.Fatalf("id changed after ordinary work: %q -> %q", first, got)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if second := gitx.WorkingCopyID(dir); second == "" || second == first {
		t.Fatalf("recreated working copy reused id: first=%q second=%q", first, second)
	}
}

func TestWorkingCopyIDUsesLinkedWorktreeGitDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "worktree")
	gitDir := filepath.Join(root, "common", "worktrees", "feature")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := gitx.WorkingCopyID(dir)
	if id == "" {
		t.Fatal("empty id")
	}
	if got := gitx.ReadWorkingCopyID(filepath.Join(gitDir, "agent-fleet-working-copy-id")); got != id {
		t.Fatalf("gitdir marker=%q want %q", got, id)
	}
}

func TestWorkingCopyIDFailsClosedWhenMarkerCannotBePersisted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "broken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A malformed .git file has filesystem identity but no safe persistent
	// metadata location. It must not receive a shareable fallback id.
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("not-a-gitdir\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := gitx.WorkingCopyID(dir); got != "" {
		t.Fatalf("unsafe fallback id=%q", got)
	}
}
