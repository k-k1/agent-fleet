package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

type tombstoneEnv struct {
	srv    *httptest.Server
	home   string
	parent string
	wt     string
}

// tombstoneSetup is a worktree app@wt-x with an unpushed commit, an uncommitted edit and an
// untracked file, and a stopped claude session in it.
func tombstoneSetup(t *testing.T) tombstoneEnv {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	mux := trashMux()
	mux.HandleFunc("GET /cleanup/archives", handleListCleanupArchives)
	mux.HandleFunc("DELETE /cleanup/archives/{id}", handlePurgeCleanupArchive)
	mux.HandleFunc("GET /repos/{name}/recreate", handleRecreatePlan)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent)
	wt, err := gitx.EnsureWorktree(parent, "main", "wt-x", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "committed.txt"), []byte("unpushed"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, wt, "add", "-A")
	gitAt(t, wt, "commit", "-q", "-m", "unpushed")
	if err := os.WriteFile(filepath.Join(wt, "f"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "untracked.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "aiaaaa1", Dir: wt, Kind: session.KindClaude, Branch: "wt-x"})
	return tombstoneEnv{srv: srv, home: home, parent: parent, wt: wt}
}

func pinnedRefs(t *testing.T, parent string) string {
	t.Helper()
	return strings.TrimSpace(gitAt(t, parent, "for-each-ref", "--format=%(refname)", gitx.DeletedWorktreeRefPrefix))
}

func worktreeArchives(t *testing.T, srv *httptest.Server) []cleanupManifest {
	t.Helper()
	var list struct{ Archives []cleanupManifest }
	do(t, srv, "GET", "/cleanup/archives", nil, http.StatusOK, &list)
	var out []cleanupManifest
	for _, a := range list.Archives {
		if a.Reason == "delete_worktree" {
			out = append(out, a)
		}
	}
	return out
}

// A worktree delete goes through the trash: restoring the entry brings the folder back at its
// path with its uncommitted work and takes its session off the shelf, and purging the entry
// is what finally lets gc have the commits.
func TestDeletedWorktreeRoundTripsThroughTheTrash(t *testing.T) {
	e := tombstoneSetup(t)
	do(t, e.srv, "DELETE", "/repos/app@wt-x?force=true", nil, http.StatusOK, nil)
	if gitx.IsGitRepo(e.wt) {
		t.Fatal("the worktree is still there")
	}
	arcs := worktreeArchives(t, e.srv)
	if len(arcs) != 1 {
		t.Fatalf("delete_worktree archives = %+v, want one", arcs)
	}
	tomb := arcs[0].Worktree
	if tomb == nil || tomb.Path != e.wt || tomb.Branch != "wt-x" || tomb.Snapshot == "" ||
		len(tomb.Shelved) != 1 || tomb.Shelved[0] != "aiaaaa1" {
		t.Fatalf("tombstone = %+v", tomb)
	}
	if refs := pinnedRefs(t, e.parent); refs != tomb.Ref {
		t.Fatalf("pinned refs = %q, want %q", refs, tomb.Ref)
	}
	if m, _ := session.ReadMeta("aiaaaa1"); !m.Archived {
		t.Fatal("the session was not shelved")
	}

	// The archive's recreate dialog offers the recorded state first.
	var plan recreatePlan
	do(t, e.srv, "GET", "/repos/app@wt-x/recreate", nil, http.StatusOK, &plan)
	if len(plan.Candidates) == 0 || plan.Candidates[0].Source != gitx.RecreateDeleted || plan.Candidates[0].Snapshot != tomb.Snapshot {
		t.Fatalf("plan = %+v, want the deleted state first", plan)
	}

	do(t, e.srv, "POST", "/cleanup/archives/"+arcs[0].ID+"/restore", nil, http.StatusOK, nil)
	if got, _ := os.ReadFile(filepath.Join(e.wt, "f")); string(got) != "edited" {
		t.Errorf("f = %q, want the uncommitted edit back", got)
	}
	if got, _ := os.ReadFile(filepath.Join(e.wt, "untracked.txt")); string(got) != "new" {
		t.Errorf("untracked = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(e.wt, "committed.txt")); string(got) != "unpushed" {
		t.Errorf("committed = %q", got)
	}
	if m, _ := session.ReadMeta("aiaaaa1"); m.Archived {
		t.Error("the shelved session was not restored with its folder")
	}

	// Purging the entry drops the pin (and nothing else).
	do(t, e.srv, "DELETE", "/cleanup/archives/"+arcs[0].ID, nil, http.StatusOK, nil)
	if refs := pinnedRefs(t, e.parent); refs != "" {
		t.Errorf("pins after purge = %q", refs)
	}
	if !gitx.IsGitRepo(e.wt) {
		t.Error("purging the entry touched the restored worktree")
	}
}

// Recording comes before removing: when the trash cannot take the entry, nothing is deleted
// and no pin is left behind.
func TestWorktreeStaysWhenItsTombstoneCannotBeWritten(t *testing.T) {
	e := tombstoneSetup(t)
	store := cleanupStoreDir() // a file where the trash directory should be
	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var res struct{ Error struct{ Code string } }
	do(t, e.srv, "DELETE", "/repos/app@wt-x?force=true", nil, http.StatusInternalServerError, &res)
	if res.Error.Code != errCodeWorktreeArchiveFailed {
		t.Fatalf("code = %q", res.Error.Code)
	}
	if got, _ := os.ReadFile(filepath.Join(e.wt, "f")); string(got) != "edited" {
		t.Fatalf("the worktree was touched: f = %q", got)
	}
	if refs := pinnedRefs(t, e.parent); refs != "" {
		t.Errorf("a pin was left behind: %q", refs)
	}
	if m, _ := session.ReadMeta("aiaaaa1"); m.Archived {
		t.Error("the session was shelved although nothing was deleted")
	}
}

// A branch that moved on after the delete cannot be checked out as "what was deleted": the
// trash refuses with a code the Console turns into "recreate it from the archive", and
// creates nothing.
func TestTrashRestoreOfAMovedBranchRefuses(t *testing.T) {
	e := tombstoneSetup(t)
	do(t, e.srv, "DELETE", "/repos/app@wt-x?force=true", nil, http.StatusOK, nil)
	gitAt(t, e.parent, "branch", "-q", "-f", "wt-x", "main")
	arcs := worktreeArchives(t, e.srv)
	var res struct{ Error struct{ Code string } }
	do(t, e.srv, "POST", "/cleanup/archives/"+arcs[0].ID+"/restore", nil, http.StatusConflict, &res)
	if res.Error.Code != errCodeRecreateNeedsNewBranch {
		t.Fatalf("code = %q", res.Error.Code)
	}
	if _, err := os.Lstat(e.wt); !os.IsNotExist(err) {
		t.Fatalf("something was created at the path: %v", err)
	}
	if m, _ := session.ReadMeta("aiaaaa1"); !m.Archived {
		t.Error("the session left the shelf although its folder did not come back")
	}
}
