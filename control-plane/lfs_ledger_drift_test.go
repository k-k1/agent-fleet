package main

import (
	"context"
	"net/http"
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
