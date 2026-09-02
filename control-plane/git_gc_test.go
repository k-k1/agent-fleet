package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestLFSGCPrune drives the real referenced-oid enumeration + orphan prune. It
// commits an LFS pointer (plain git, no git-lfs needed) so one oid is referenced,
// seeds a referenced object + an orphan object + a young orphan on disk, and checks
// that only the aged, unreferenced object is pruned (file + ledger), quota freed.
func TestLFSGCPrune(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	tmp := t.TempDir()
	dataRoot := filepath.Join(tmp, "data")
	st, err := openSQLite(filepath.Join(tmp, "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	dflt, _ := st.EnsureDefaultTenant(ctx)
	if err := st.CreateGitRepo(ctx, GitRepo{ID: newID(), TenantID: dflt.ID, Name: "shared", DefaultBranch: "main", CreatedAt: nowTS()}); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(dataRoot, "git", "default", "shared.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o700); err != nil {
		t.Fatal(err)
	}
	gitRun(t, tmp, nil, "init", "--bare", "--initial-branch=main", bare)

	env := []string{"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x"}

	// Commit an LFS pointer referencing oidRef (plain git — the pointer is just text).
	oidRef := oidOf([]byte("the-referenced-object"))
	wc := filepath.Join(tmp, "wc")
	gitRun(t, tmp, env, "clone", bare, wc)
	pointer := fmt.Sprintf("version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize 21\n", oidRef)
	if err := os.WriteFile(filepath.Join(wc, "asset.bin"), []byte(pointer), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, wc, env, "add", "asset.bin")
	gitRun(t, wc, env, "commit", "-m", "add lfs pointer")
	gitRun(t, wc, env, "push", "origin", "HEAD:main")

	// Enumeration must see the referenced oid.
	ref, err := referencedLFSOIDs(ctx, bare)
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if !ref[oidRef] {
		t.Fatalf("referenced oid %s not found; got %v", oidRef, ref)
	}

	// Seed object files: referenced (aged), orphan (aged), orphan (young).
	oidOrphan := oidOf([]byte("orphaned-object"))
	oidYoung := oidOf([]byte("young-orphan-object"))
	seed := func(oid string, size int64, age time.Duration) {
		p := filepath.Join(bare, "lfs", "objects", oid[0:2], oid[2:4], oid)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, bytes.Repeat([]byte("x"), int(size)), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(-age)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
		if err := st.PutLFSObject(ctx, dflt.ID, "shared", oid, size); err != nil {
			t.Fatal(err)
		}
	}
	seed(oidRef, 100, 2*time.Hour)    // referenced → keep regardless of age
	seed(oidOrphan, 200, 2*time.Hour) // aged orphan → prune
	seed(oidYoung, 50, 0)             // young orphan → grace keeps it

	if n, _ := st.TenantLFSBytes(ctx, dflt.ID); n != 350 {
		t.Fatalf("pre-GC ledger bytes = %d, want 350", n)
	}

	g := newGitGC(st, dataRoot, 0, time.Hour) // grace = 1h
	g.pruneLFS(ctx, "default", "shared", bare)

	exists := func(oid string) bool {
		_, err := os.Stat(filepath.Join(bare, "lfs", "objects", oid[0:2], oid[2:4], oid))
		return err == nil
	}
	if !exists(oidRef) {
		t.Error("referenced object was pruned")
	}
	if exists(oidOrphan) {
		t.Error("aged orphan was NOT pruned")
	}
	if !exists(oidYoung) {
		t.Error("young orphan was pruned despite grace window")
	}
	// Ledger: orphan row gone (quota freed), the other two remain.
	if n, _ := st.TenantLFSBytes(ctx, dflt.ID); n != 150 { // 100 (ref) + 50 (young)
		t.Fatalf("post-GC ledger bytes = %d, want 150", n)
	}
	oids, _ := st.ListLFSObjectOIDs(ctx, dflt.ID, "shared")
	for _, o := range oids {
		if o == oidOrphan {
			t.Error("orphan ledger row not deleted")
		}
	}
}

// TestReferencedLFSOIDsEmpty: a repo with no pointer blobs yields an empty set,
// not an error (so pruneLFS treats everything as orphan-eligible correctly).
func TestReferencedLFSOIDsEmpty(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "r.git")
	gitRun(t, tmp, nil, "init", "--bare", bare)
	ref, err := referencedLFSOIDs(context.Background(), bare)
	if err != nil {
		t.Fatalf("enumerate empty: %v", err)
	}
	if len(ref) != 0 {
		t.Fatalf("empty repo should reference nothing, got %v", ref)
	}
}
