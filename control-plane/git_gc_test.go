package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// lfsGCFixture is a bare repo whose history commits one LFS pointer (plain git, no
// git-lfs needed), so oidRef is referenced, plus the ledger the prune reconciles.
type lfsGCFixture struct {
	st       *store.SQL
	tenantID string
	dataRoot string
	bare     string
	oidRef   string
}

func newLFSGCFixture(t *testing.T) *lfsGCFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	tmp := t.TempDir()
	dataRoot := filepath.Join(tmp, "data")
	st, err := store.OpenSQLite(filepath.Join(tmp, "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	dflt, _ := st.EnsureDefaultTenant(ctx)
	if err := st.CreateGitRepo(ctx, store.GitRepo{ID: store.NewID(), TenantID: dflt.ID, Name: "shared", DefaultBranch: "main", CreatedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(dataRoot, "git", "default", "shared.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o700); err != nil {
		t.Fatal(err)
	}
	gitRun(t, tmp, nil, "init", "--bare", "--initial-branch=main", bare)

	env := []string{"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x"}
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
	return &lfsGCFixture{st: st, tenantID: dflt.ID, dataRoot: dataRoot, bare: bare, oidRef: oidRef}
}

func (f *lfsGCFixture) objPath(oid string) string {
	return filepath.Join(f.bare, "lfs", "objects", oid[0:2], oid[2:4], oid)
}

// seed writes an object file aged by age and records it in the ledger.
func (f *lfsGCFixture) seed(t *testing.T, oid string, size int64, age time.Duration) {
	t.Helper()
	p := f.objPath(oid)
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
	if err := f.st.PutLFSObject(context.Background(), f.tenantID, "shared", oid, size); err != nil {
		t.Fatal(err)
	}
}

func (f *lfsGCFixture) exists(oid string) bool {
	_, err := os.Stat(f.objPath(oid))
	return err == nil
}

// TestLFSGCPrune drives the real referenced-oid enumeration + orphan prune: of a
// referenced object, an aged orphan and a young orphan, only the aged, unreferenced
// object is pruned (file + ledger), quota freed.
func TestLFSGCPrune(t *testing.T) {
	f := newLFSGCFixture(t)
	ctx := context.Background()

	// Enumeration must see the referenced oid.
	ref, err := referencedLFSOIDs(ctx, f.bare)
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if !ref[f.oidRef] {
		t.Fatalf("referenced oid %s not found; got %v", f.oidRef, ref)
	}

	oidOrphan := oidOf([]byte("orphaned-object"))
	oidYoung := oidOf([]byte("young-orphan-object"))
	f.seed(t, f.oidRef, 100, 2*time.Hour)  // referenced → keep regardless of age
	f.seed(t, oidOrphan, 200, 2*time.Hour) // aged orphan → prune
	f.seed(t, oidYoung, 50, 0)             // young orphan → grace keeps it

	if n, _ := f.st.TenantLFSBytes(ctx, f.tenantID); n != 350 {
		t.Fatalf("pre-GC ledger bytes = %d, want 350", n)
	}

	g := newGitGC(f.st, f.dataRoot, 0, time.Hour) // grace = 1h
	g.pruneLFS(ctx, "default", "shared", f.bare)

	if !f.exists(f.oidRef) {
		t.Error("referenced object was pruned")
	}
	if f.exists(oidOrphan) {
		t.Error("aged orphan was NOT pruned")
	}
	if !f.exists(oidYoung) {
		t.Error("young orphan was pruned despite grace window")
	}
	// Ledger: orphan row gone (quota freed), the other two remain.
	if n, _ := f.st.TenantLFSBytes(ctx, f.tenantID); n != 150 { // 100 (ref) + 50 (young)
		t.Fatalf("post-GC ledger bytes = %d, want 150", n)
	}
	rows, _ := f.st.ListLFSObjects(ctx, f.tenantID, "shared")
	for _, o := range rows {
		if o.OID == oidOrphan {
			t.Error("orphan ledger row not deleted")
		}
	}
}

// TestLFSGCPrunePass2Failure: when the pointer stream dies part-way or cat-file exits
// non-zero, enumeration fails and the prune deletes nothing — not even a genuine
// orphan — rather than reading every unread pointer as unreferenced.
func TestLFSGCPrunePass2Failure(t *testing.T) {
	cases := map[string]struct{ script, wantErr string }{
		// head exits after a few bytes; the pipeline's status is head's 0, so only
		// the short stream gives the failure away.
		"truncated stream": {`git --git-dir "$1" cat-file --batch | head -c 20`, "response 1 of 1"},
		"non-zero exit":    {`git --git-dir "$1" cat-file --batch; exit 3`, "exit status 3"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newLFSGCFixture(t)
			ctx := context.Background()
			orig := lfsCatFileBatch
			t.Cleanup(func() { lfsCatFileBatch = orig })
			lfsCatFileBatch = func(ctx context.Context, bareDir string) *exec.Cmd {
				return exec.CommandContext(ctx, "sh", "-c", tc.script, "sh", bareDir)
			}

			ref, err := referencedLFSOIDs(ctx, f.bare)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("enumerate = %v, %v; want an error containing %q", ref, err, tc.wantErr)
			}

			oidOrphan := oidOf([]byte("orphaned-object"))
			f.seed(t, f.oidRef, 100, 2*time.Hour)
			f.seed(t, oidOrphan, 200, 2*time.Hour)
			newGitGC(f.st, f.dataRoot, 0, time.Hour).pruneLFS(ctx, "default", "shared", f.bare)
			if !f.exists(f.oidRef) {
				t.Error("referenced object was pruned after a failed enumeration")
			}
			if !f.exists(oidOrphan) {
				t.Error("orphan was pruned after a failed enumeration")
			}
			if n, _ := f.st.TenantLFSBytes(ctx, f.tenantID); n != 300 {
				t.Errorf("ledger bytes = %d, want 300 (nothing deleted)", n)
			}
		})
	}
}

// TestReferencedLFSOIDsIgnoresReplaceRefs: a refs/replace entry swapping the pointer
// blob for a small non-pointer blob must not hide the pointer from enumeration.
func TestReferencedLFSOIDsIgnoresReplaceRefs(t *testing.T) {
	f := newLFSGCFixture(t)
	pointerBlob := gitOut(t, f.bare, "--git-dir", f.bare, "rev-parse", "main:asset.bin")
	decoy := filepath.Join(t.TempDir(), "decoy")
	if err := os.WriteFile(decoy, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	decoyBlob := gitOut(t, f.bare, "--git-dir", f.bare, "hash-object", "-w", decoy)
	gitRun(t, f.bare, nil, "--git-dir", f.bare, "replace", "-f", pointerBlob, decoyBlob)

	ref, err := referencedLFSOIDs(context.Background(), f.bare)
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if !ref[f.oidRef] {
		t.Fatalf("replace ref hid the pointer: got %v, want %s", ref, f.oidRef)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// TestReadPointerBatch: every prefix of a valid response stream, and every response
// that does not answer the candidate asked, is an error rather than a partial set.
func TestReadPointerBatch(t *testing.T) {
	shaA := strings.Repeat("a", 40)
	shaB := strings.Repeat("b", 40)
	oid := oidOf([]byte("obj"))
	pointer := fmt.Sprintf("version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize 3\n", oid)
	answer := func(sha, body string) string {
		return fmt.Sprintf("%s blob %d\n%s\n", sha, len(body), body)
	}
	full := answer(shaA, "not a pointer") + answer(shaB, pointer)
	candidates := []string{shaA, shaB}

	ref, err := readPointerBatch(strings.NewReader(full), candidates)
	if err != nil {
		t.Fatalf("full stream: %v", err)
	}
	if len(ref) != 1 || !ref[oid] {
		t.Fatalf("full stream = %v, want only %s", ref, oid)
	}

	for n := 0; n < len(full); n++ {
		if ref, err := readPointerBatch(strings.NewReader(full[:n]), candidates); err == nil {
			t.Fatalf("prefix of %d/%d bytes returned %v, want an error", n, len(full), ref)
		}
	}

	bad := map[string]string{
		"missing":          answer(shaA, "x") + shaB + " missing\n",
		"wrong sha":        answer(shaB, pointer) + answer(shaA, "x"),
		"not a blob":       answer(shaA, "x") + shaB + " tree 5\nxxxxx\n",
		"bad size":         answer(shaA, "x") + shaB + " blob ten\n",
		"oversized":        answer(shaA, "x") + fmt.Sprintf("%s blob %d\n", shaB, pointerMaxBytes+1),
		"no LF after body": answer(shaA, "x") + fmt.Sprintf("%s blob 2\nxyz", shaB),
	}
	for name, stream := range bad {
		if ref, err := readPointerBatch(strings.NewReader(stream), candidates); err == nil {
			t.Errorf("%s: returned %v, want an error", name, ref)
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

// backdate moves a ledger row's created_at into the past, as if it were written age ago.
func (f *lfsGCFixture) backdate(t *testing.T, oid string, age time.Duration) {
	t.Helper()
	at := time.Now().UTC().Add(-age).Format(time.RFC3339)
	if _, err := f.st.DB().ExecContext(context.Background(),
		`UPDATE lfs_object SET created_at=? WHERE tenant_id=? AND repo_name=? AND oid=?`, at, f.tenantID, "shared", oid); err != nil {
		t.Fatal(err)
	}
}

// putRow records a ledger row with no object file behind it: the publish failed, or the
// process died before the rename.
func (f *lfsGCFixture) putRow(t *testing.T, oid string, size int64, age time.Duration) {
	t.Helper()
	if err := f.st.PutLFSObject(context.Background(), f.tenantID, "shared", oid, size); err != nil {
		t.Fatal(err)
	}
	f.backdate(t, oid, age)
}

func (f *lfsGCFixture) ledger(t *testing.T) map[string]bool {
	t.Helper()
	rows, err := f.st.ListLFSObjects(context.Background(), f.tenantID, "shared")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, o := range rows {
		out[o.OID] = true
	}
	return out
}

// Issue #1335: a ledger row whose object never landed on disk is removed once it is older
// than the grace window, so it stops counting against the tenant's quota. A fresh one, and
// one whose upload was just retried, may belong to an upload still in flight and stay. The
// repo has no lfs/objects directory at all, which is what a first upload that never
// published leaves.
func TestLFSGCReconcilesLedgerRowsWithoutAFile(t *testing.T) {
	f := newLFSGCFixture(t)
	ctx := context.Background()
	oidStale := oidOf([]byte("never-published"))
	oidFresh := oidOf([]byte("publishing-now"))
	oidRetried := oidOf([]byte("retried-just-now"))
	f.putRow(t, oidStale, 200, 2*time.Hour)
	f.putRow(t, oidFresh, 50, 0)
	f.putRow(t, oidRetried, 30, 2*time.Hour)
	if err := f.st.PutLFSObject(ctx, f.tenantID, "shared", oidRetried, 30); err != nil {
		t.Fatal(err)
	}

	newGitGC(f.st, f.dataRoot, 0, time.Hour).pruneLFS(ctx, "default", "shared", f.bare)

	got := f.ledger(t)
	if got[oidStale] {
		t.Error("a ledger row older than the grace window with no object file was kept")
	}
	if !got[oidFresh] {
		t.Error("a fresh ledger row was removed; its upload may still be in flight")
	}
	if !got[oidRetried] {
		t.Error("a ledger row whose upload was just retried was removed")
	}
	if n, _ := f.st.TenantLFSBytes(ctx, f.tenantID); n != 80 {
		t.Errorf("ledger bytes = %d, want 80 (50 fresh + 30 retried)", n)
	}
}

// A row whose file is on disk is never reconciled away, however old.
func TestLFSGCReconcileKeepsRowsWithAFile(t *testing.T) {
	f := newLFSGCFixture(t)
	ctx := context.Background()
	f.seed(t, f.oidRef, 100, 2*time.Hour)
	f.backdate(t, f.oidRef, 2*time.Hour)
	newGitGC(f.st, f.dataRoot, 0, time.Hour).pruneLFS(ctx, "default", "shared", f.bare)
	if !f.ledger(t)[f.oidRef] || !f.exists(f.oidRef) {
		t.Error("a referenced object with its file on disk lost its row or its file")
	}
}

// ledgerFailingStore fails one ledger call and passes everything else through.
type ledgerFailingStore struct {
	*store.SQL
	failList, failDelete bool
}

func (s ledgerFailingStore) ListLFSObjects(ctx context.Context, tenantID, repo string) ([]store.LFSObject, error) {
	if s.failList {
		return nil, errors.New("lfs_object: database is locked")
	}
	return s.SQL.ListLFSObjects(ctx, tenantID, repo)
}

func (s ledgerFailingStore) DeleteStaleLFSObject(ctx context.Context, tenantID, repo, oid, cutoff string) (bool, error) {
	if s.failDelete {
		return false, errors.New("lfs_object: database is locked")
	}
	return s.SQL.DeleteStaleLFSObject(ctx, tenantID, repo, oid, cutoff)
}

// A store error in the reconcile aborts the whole pass: no ledger row and no object file is
// touched, not even an orphan the prune would otherwise delete.
func TestLFSGCReconcileStoreErrorAbortsThePass(t *testing.T) {
	for name, st := range map[string]func(*store.SQL) ledgerFailingStore{
		"list fails":   func(s *store.SQL) ledgerFailingStore { return ledgerFailingStore{SQL: s, failList: true} },
		"delete fails": func(s *store.SQL) ledgerFailingStore { return ledgerFailingStore{SQL: s, failDelete: true} },
	} {
		t.Run(name, func(t *testing.T) {
			f := newLFSGCFixture(t)
			ctx := context.Background()
			oidStale := oidOf([]byte("never-published"))
			oidOrphan := oidOf([]byte("orphaned-object"))
			f.putRow(t, oidStale, 200, 2*time.Hour)
			f.seed(t, oidOrphan, 100, 2*time.Hour)

			newGitGC(st(f.st), f.dataRoot, 0, time.Hour).pruneLFS(ctx, "default", "shared", f.bare)

			got := f.ledger(t)
			if !got[oidStale] || !got[oidOrphan] {
				t.Errorf("ledger after a store error = %v, want both rows untouched", got)
			}
			if !f.exists(oidOrphan) {
				t.Error("the orphan's file was pruned although the pass hit a store error")
			}
		})
	}
}

// A rename moves the bare before RenameGitRepo moves the rows. A sweep that reads the rows
// under the old name in between must not read every object as absent and drop the rows,
// which would under-count the quota for good (batch reports the objects as present, so they
// are never uploaded again).
func TestLFSGCReconcileLeavesTheRowsOfARepoMovedMidSweep(t *testing.T) {
	f := newLFSGCFixture(t)
	ctx := context.Background()
	oid := oidOf([]byte("moved-with-the-repo"))
	f.seed(t, oid, 100, 2*time.Hour)
	f.backdate(t, oid, 2*time.Hour)
	if err := os.Rename(f.bare, filepath.Join(filepath.Dir(f.bare), "renamed.git")); err != nil {
		t.Fatal(err)
	}
	if err := newGitGC(f.st, f.dataRoot, 0, time.Hour).reconcileLFSLedger(ctx, f.tenantID, "default", "shared", f.bare); err != nil {
		t.Fatal(err)
	}
	if !f.ledger(t)[oid] {
		t.Error("the row of an object that moved with its repo was dropped")
	}
}

// With no grace period the reconcile does not run: a row written this second may belong to
// an upload between its ledger write and its rename.
func TestLFSGCReconcileNeedsAGracePeriod(t *testing.T) {
	f := newLFSGCFixture(t)
	oid := oidOf([]byte("publishing-this-second"))
	f.putRow(t, oid, 10, 0)
	newGitGC(f.st, f.dataRoot, 0, 0).pruneLFS(context.Background(), "default", "shared", f.bare)
	if !f.ledger(t)[oid] {
		t.Error("with AF_LFS_GC_GRACE=0 the reconcile dropped a row written this second")
	}
}
