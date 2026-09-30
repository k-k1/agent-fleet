package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// failOn makes every <op> on table fail from now on (SQLite trigger), standing in for
// a store error part-way through a ledger update.
func failOn(t *testing.T, st *store.SQL, op, table string) {
	t.Helper()
	_, err := st.DB().ExecContext(context.Background(),
		`CREATE TRIGGER fail_`+op+`_`+table+` BEFORE `+op+` ON `+table+` BEGIN SELECT RAISE(ABORT, 'injected'); END`)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
}

func countWhere(t *testing.T, st *store.SQL, q string, args ...any) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func seedRepoLedger(t *testing.T, st *store.SQL, tenantID, repo string) {
	t.Helper()
	ctx := context.Background()
	if err := st.PutLFSObject(ctx, tenantID, repo, oidOf([]byte(repo)), 10); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateLFSLock(ctx, store.LFSLock{ID: store.NewID(), TenantID: tenantID, RepoName: repo,
		Path: "a.bin", OwnerID: "m", OwnerName: "o", LockedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
}

// Deleting a repo takes its LFS ledger and lock rows along; renaming carries them.
// Otherwise the quota over-counts and a later repo of the same name inherits locks.
func TestGitRepoDeleteRenameCarryLFSRows(t *testing.T) {
	e := newLFSEnv(t)
	ctx := context.Background()
	dflt, _, _ := e.st.GetTenantBySlug(ctx, "default")
	seedRepoLedger(t, e.st, dflt.ID, "shared")

	if err := e.st.RenameGitRepo(ctx, dflt.ID, "shared", "moved"); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"lfs_object", "lfs_lock"} {
		if n := countWhere(t, e.st, `SELECT COUNT(*) FROM `+tbl+` WHERE repo_name='moved'`); n != 1 {
			t.Errorf("after rename, %s rows under the new name = %d, want 1", tbl, n)
		}
	}
	if err := e.st.DeleteGitRepo(ctx, dflt.ID, "moved"); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"lfs_object", "lfs_lock"} {
		if n := countWhere(t, e.st, `SELECT COUNT(*) FROM `+tbl); n != 0 {
			t.Errorf("after delete, %d %s rows remain", n, tbl)
		}
	}
}

// A store error part-way leaves the repo row, the ledger and the locks as they were,
// so the handler can fail the request before touching the disk.
func TestGitRepoDeleteIsAtomic(t *testing.T) {
	e := newLFSEnv(t)
	ctx := context.Background()
	dflt, _, _ := e.st.GetTenantBySlug(ctx, "default")
	seedRepoLedger(t, e.st, dflt.ID, "shared")
	failOn(t, e.st, "DELETE", "lfs_lock")

	if err := e.st.DeleteGitRepo(ctx, dflt.ID, "shared"); err == nil {
		t.Fatal("DeleteGitRepo succeeded although the lock rows could not be deleted")
	}
	if _, ok, _ := e.st.GetGitRepo(ctx, dflt.ID, "shared"); !ok {
		t.Error("the repo row went although the transaction failed")
	}
	if n := countWhere(t, e.st, `SELECT COUNT(*) FROM lfs_object`); n != 1 {
		t.Errorf("lfs_object rows = %d after a failed delete, want 1", n)
	}
}

// A ledger write that fails must fail the upload and leave the object unpublished:
// once published, batch reports it present and the row would never be written.
func TestLFSUploadLedgerFailureDoesNotPublish(t *testing.T) {
	e := newLFSEnv(t)
	failOn(t, e.st, "INSERT", "lfs_object")
	data := []byte("payload")
	oid := oidOf(data)
	w := e.upload(oid, data)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("upload with a failing ledger: got %d, want 500", w.Code)
	}
	if d := e.download(oid); d.Code != http.StatusNotFound {
		t.Fatalf("object was published without a ledger row (download %d)", d.Code)
	}
}

// GC keeps an orphan whose ledger row cannot be deleted; removing the file first would
// leave a row that over-counts the quota with nothing left to reconcile it against.
func TestLFSGCKeepsObjectWhenLedgerDeleteFails(t *testing.T) {
	f := newLFSGCFixture(t)
	ctx := context.Background()
	orphan := oidOf([]byte("orphaned-object"))
	f.seed(t, orphan, 200, 2*time.Hour)
	failOn(t, f.st, "DELETE", "lfs_object")

	newGitGC(f.st, f.dataRoot, 0, time.Hour).pruneLFS(ctx, "default", "shared", f.bare)

	if !f.exists(orphan) {
		t.Fatal("orphan file removed although its ledger row could not be deleted")
	}
}

// hookedLFSStore wraps the real store to inject failures and interleavings into the
// upload path.
type hookedLFSStore struct {
	gitServerStore
	afterPut func()
	onDelete func()
}

func (s hookedLFSStore) PutLFSObject(ctx context.Context, tenant, repo, oid string, size int64) error {
	if err := s.gitServerStore.PutLFSObject(ctx, tenant, repo, oid, size); err != nil {
		return err
	}
	if s.afterPut != nil {
		s.afterPut()
	}
	return nil
}

func (s hookedLFSStore) DeleteLFSObject(ctx context.Context, tenant, repo, oid string) error {
	if s.onDelete != nil {
		s.onDelete()
	}
	return s.gitServerStore.DeleteLFSObject(ctx, tenant, repo, oid)
}

// A failed publish keeps the ledger row. Upload B runs at the point where a rollback
// of A's row would happen: B's own ledger write is a no-op because A's row exists, so
// deleting that row would leave B's published object unaccounted for.
func TestLFSUploadPublishFailureKeepsConcurrentAccounting(t *testing.T) {
	e := newLFSEnv(t)
	data := []byte("concurrent-upload")
	oid := oidOf(data)
	real := e.g.store
	first, codeB := true, 0
	runB := func() {
		if codeB == 0 {
			e.g.store = real // B takes the plain path
			codeB = e.upload(oid, data).Code
		}
	}
	e.g.store = hookedLFSStore{gitServerStore: real,
		afterPut: func() {
			if !first {
				return
			}
			first = false
			// Make A's rename fail: its temp file disappears after the ledger write.
			temps, _ := filepath.Glob(filepath.Join(filepath.Dir(e.g.lfsObjectPath("default", "shared", oid)), ".upload-*"))
			if len(temps) != 1 {
				t.Fatalf("cannot inject a publish failure: %v", temps)
			}
			if err := os.Remove(temps[0]); err != nil {
				t.Fatal(err)
			}
		},
		onDelete: runB,
	}
	if code := e.upload(oid, data).Code; code != http.StatusInternalServerError {
		t.Fatalf("upload A with a failing publish: got %d, want 500", code)
	}
	runB() // no rollback happened, so B runs after A instead
	if codeB != http.StatusOK || !fileExists(e.g.lfsObjectPath("default", "shared", oid)) {
		t.Fatalf("upload B: got %d, published=%v", codeB, fileExists(e.g.lfsObjectPath("default", "shared", oid)))
	}
	dflt, _, _ := e.st.GetTenantBySlug(context.Background(), "default")
	if n, _ := e.st.TenantLFSBytes(context.Background(), dflt.ID); n != int64(len(data)) {
		t.Fatalf("ledger bytes = %d with B's object on disk, want %d", n, len(data))
	}
}

// When GC cannot unlink an orphan after deleting its row, the row comes back: the file
// is still there and nothing else would record it again.
func TestLFSGCRemoveFailureKeepsAccounting(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permission this relies on")
	}
	f := newLFSGCFixture(t)
	orphan := oidOf([]byte("orphaned-object"))
	f.seed(t, orphan, 200, 2*time.Hour)
	shard := filepath.Dir(f.objPath(orphan))
	if err := os.Chmod(shard, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(shard, 0o700) })

	newGitGC(f.st, f.dataRoot, 0, time.Hour).pruneLFS(context.Background(), "default", "shared", f.bare)

	if !f.exists(orphan) {
		t.Fatal("the unlink failure was not injected")
	}
	if n, _ := f.st.TenantLFSBytes(context.Background(), f.tenantID); n != 200 {
		t.Fatalf("ledger bytes = %d with the object still on disk, want 200", n)
	}
}

// failFirstPublish makes the first upload's rename fail after its ledger write.
func failFirstPublish(t *testing.T, e *lfsEnv, oid string) {
	t.Helper()
	real, first := e.g.store, true
	e.g.store = hookedLFSStore{gitServerStore: real, afterPut: func() {
		if !first {
			return
		}
		first = false
		temps, _ := filepath.Glob(filepath.Join(filepath.Dir(e.g.lfsObjectPath("default", "shared", oid)), ".upload-*"))
		if len(temps) != 1 {
			t.Fatalf("cannot inject a publish failure: %v", temps)
		}
		if err := os.Remove(temps[0]); err != nil {
			t.Fatal(err)
		}
	}}
}

// The row a failed publish keeps is credited to the retry of the same oid; otherwise
// at a full quota the object could never be uploaded again.
func TestLFSRetryAfterFailedPublishAtQuota(t *testing.T) {
	e := newLFSEnv(t)
	data := []byte("retry-at-quota")
	oid := oidOf(data)
	e.setLFSCap(t, int64(len(data)))
	failFirstPublish(t, e, oid)
	if code := e.upload(oid, data).Code; code != http.StatusInternalServerError {
		t.Fatalf("first upload: got %d, want 500", code)
	}

	out := e.batch(t, "upload", []map[string]any{{"oid": oid, "size": len(data)}})
	obj := out["objects"].([]any)[0].(map[string]any)
	if _, ok := obj["actions"]; !ok {
		t.Fatalf("batch at quota refused the retry: %v", obj)
	}
	if w := e.upload(oid, data); w.Code != http.StatusOK {
		t.Fatalf("retry upload: got %d (%s), want 200", w.Code, w.Body.String())
	}
	dflt, _, _ := e.st.GetTenantBySlug(context.Background(), "default")
	if n, _ := e.st.TenantLFSBytes(context.Background(), dflt.ID); n != int64(len(data)) {
		t.Fatalf("ledger bytes = %d after the retry, want %d", n, len(data))
	}
	// Any other object still meets the full quota.
	other := []byte("x")
	out = e.batch(t, "upload", []map[string]any{{"oid": oidOf(other), "size": 1}})
	if _, ok := out["objects"].([]any)[0].(map[string]any)["error"]; !ok {
		t.Fatalf("a new object was admitted over the quota: %v", out)
	}
}

type hookedGCStore struct {
	gitGCStore
	afterDelete func()
}

func (s hookedGCStore) DeleteLFSObject(ctx context.Context, tenant, repo, oid string) error {
	if err := s.gitGCStore.DeleteLFSObject(ctx, tenant, repo, oid); err != nil {
		return err
	}
	s.afterDelete()
	return nil
}

// A repo delete that lands between GC's row delete and its unlink leaves ENOENT; that
// is a finished removal, not a failure whose row should be restored.
func TestLFSGCDoesNotRestoreRowOfDeletedRepo(t *testing.T) {
	f := newLFSGCFixture(t)
	ctx := context.Background()
	orphan := oidOf([]byte("orphaned-object"))
	f.seed(t, orphan, 200, 2*time.Hour)
	st := hookedGCStore{gitGCStore: f.st, afterDelete: func() {
		if err := f.st.DeleteGitRepo(ctx, f.tenantID, "shared"); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(f.bare); err != nil {
			t.Fatal(err)
		}
	}}
	newGitGC(st, f.dataRoot, 0, time.Hour).pruneLFS(ctx, "default", "shared", f.bare)
	if n, _ := f.st.TenantLFSBytes(ctx, f.tenantID); n != 0 {
		t.Fatalf("GC restored %d ledger bytes for a deleted repo", n)
	}
}
