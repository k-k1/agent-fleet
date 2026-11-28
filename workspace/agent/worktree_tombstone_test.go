package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
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
	mux.HandleFunc("POST /repos/{name}/recreate", handleRecreateWorktree)
	mux.HandleFunc("POST /sessions/{name}/archive", sessionx.HandleArchiveSession)
	mux.HandleFunc("POST /sessions/{name}/restore", sessionx.HandleRestoreSession)
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
	// Started in a subfolder: shelved by the delete as well, so it has to come back too.
	session.WriteMeta(session.Meta{Name: "aisub01", Dir: filepath.Join(wt, "sub"), Kind: session.KindClaude, Branch: "wt-x"})
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
	if err := gitx.WriteWorktreeCreator(e.wt, "sess-a"); err != nil {
		t.Fatal(err)
	}
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
		strings.Join(tomb.Shelved, ",") != "aiaaaa1,aisub01" {
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
	for _, name := range []string{"aiaaaa1", "aisub01"} {
		if m, _ := session.ReadMeta(name); m.Archived {
			t.Errorf("shelved session %s was not restored with its folder", name)
		}
	}
	if got := gitx.ReadWorktreeCreator(e.wt); got != "sess-a" {
		t.Errorf("restored creator = %q, want sess-a", got)
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

// A worktree restore that stopped after its sessions were back, retried after the member shelved
// one of them again, leaves that one on the shelf; so does pressing restore again once it has
// finished. Older Agents wrote no shelf revision, and the empty one still counts.
func TestWorktreeRestoreRetryKeepsALaterShelfAction(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	e := tombstoneSetup(t)
	do(t, e.srv, "DELETE", "/repos/app@wt-x?force=true", nil, http.StatusOK, nil)
	arcs := worktreeArchives(t, e.srv)
	if len(arcs) != 1 {
		t.Fatalf("delete_worktree archives = %+v, want one", arcs)
	}
	legacy, _ := session.ReadMeta("aisub01")
	legacy.ShelfRevision = ""
	if err := session.WriteMetaChecked(legacy); err != nil {
		t.Fatal(err)
	}
	store := cleanupStoreDir()
	worktreeBeforeRestoreComplete = func() {
		if err := os.Chmod(store, 0o500); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { worktreeBeforeRestoreComplete = nil; _ = os.Chmod(store, 0o700) })
	path := "/cleanup/archives/" + arcs[0].ID + "/restore"
	if code := errCodeOf(t, e.srv, "POST", path, http.StatusConflict); code != "restore_incomplete" {
		t.Fatalf("restore code = %q, want restore_incomplete", code)
	}
	worktreeBeforeRestoreComplete = nil
	if err := os.Chmod(store, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"aiaaaa1", "aisub01"} {
		if m, ok := session.ReadMeta(name); !ok || m.Archived {
			t.Fatalf("session %s was not back before the failed completion: %+v", name, m)
		}
	}

	do(t, e.srv, "POST", "/sessions/aiaaaa1/archive", nil, http.StatusOK, nil)
	do(t, e.srv, "POST", path, nil, http.StatusOK, nil)
	if m, ok := session.ReadMeta("aiaaaa1"); !ok || !m.Archived {
		t.Fatalf("the retry undid the newer shelf action: %+v", m)
	}
	if !gitx.IsGitRepo(e.wt) {
		t.Fatal("the worktree is not back")
	}

	do(t, e.srv, "POST", "/sessions/aisub01/archive", nil, http.StatusOK, nil)
	do(t, e.srv, "POST", path, nil, http.StatusOK, nil)
	if m, ok := session.ReadMeta("aisub01"); !ok || !m.Archived {
		t.Fatalf("restoring a finished entry again undid a shelf action: %+v", m)
	}

	do(t, e.srv, "DELETE", "/cleanup/archives/"+arcs[0].ID, nil, http.StatusOK, nil)
	record := filepath.Join(store, worktreeRestoreRecordName(arcs[0].ID))
	if _, err := os.Lstat(record); !os.IsNotExist(err) {
		t.Fatalf("purge left the restore record behind: %v", err)
	}
}

// worktreeDeletedWithEntry deletes app@wt-x and answers its trash entry's id.
func worktreeDeletedWithEntry(t *testing.T, e tombstoneEnv) string {
	t.Helper()
	do(t, e.srv, "DELETE", "/repos/app@wt-x?force=true", nil, http.StatusOK, nil)
	arcs := worktreeArchives(t, e.srv)
	if len(arcs) != 1 {
		t.Fatalf("delete_worktree archives = %+v, want one", arcs)
	}
	return arcs[0].ID
}

// A purge that stops before the archive is gone keeps the restore's progress, so restoring the
// entry again does not take off the shelf a session the member shelved after the first restore.
func TestWorktreePurgeStoppedPartWayKeepsRestoreProgress(t *testing.T) {
	e := tombstoneSetup(t)
	id := worktreeDeletedWithEntry(t, e)
	path := "/cleanup/archives/" + id + "/restore"
	do(t, e.srv, "POST", path, nil, http.StatusOK, nil)
	do(t, e.srv, "POST", "/sessions/aiaaaa1/archive", nil, http.StatusOK, nil)

	// The sidecar turned into a directory that cannot be removed: the purge stops there.
	sidecar := filepath.Join(cleanupStoreDir(), id+".json")
	data, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sidecar); err != nil {
		t.Fatal(err)
	}
	writeAt(t, filepath.Join(sidecar, "x"), "")
	if code := httpStatus(t, e.srv, "DELETE", "/cleanup/archives/"+id, nil); code == http.StatusOK {
		t.Fatal("the purge removed a directory it should have stopped at")
	}
	if err := os.RemoveAll(sidecar); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sidecar, data, 0o600); err != nil {
		t.Fatal(err)
	}
	do(t, e.srv, "POST", path, nil, http.StatusOK, nil)
	if m, ok := session.ReadMeta("aiaaaa1"); !ok || !m.Archived {
		t.Fatalf("restoring after a stopped purge undid the newer shelf action: %+v", m)
	}
}

// A purge of the same entry that wins between the folder coming back and its sessions does not
// strand the sessions on the shelf, and leaves no restore record behind.
func TestWorktreeRestoreFinishesItsSessionsAfterAPurgeWins(t *testing.T) {
	e := tombstoneSetup(t)
	id := worktreeDeletedWithEntry(t, e)
	worktreeRestoreRecreated = func() {
		if code := httpStatus(t, e.srv, "DELETE", "/cleanup/archives/"+id, nil); code != http.StatusOK {
			t.Errorf("purge between folder and sessions = %d, want 200", code)
		}
	}
	t.Cleanup(func() { worktreeRestoreRecreated = nil })
	do(t, e.srv, "POST", "/cleanup/archives/"+id+"/restore", nil, http.StatusOK, nil)
	if !gitx.IsGitRepo(e.wt) {
		t.Fatal("the worktree is not back")
	}
	for _, name := range []string{"aiaaaa1", "aisub01"} {
		if m, ok := session.ReadMeta(name); !ok || m.Archived {
			t.Errorf("session %s was left on the shelf: %+v", name, m)
		}
	}
	if _, err := os.Lstat(filepath.Join(cleanupStoreDir(), worktreeRestoreRecordName(id))); !os.IsNotExist(err) {
		t.Errorf("a restore record outlived the purged entry: %v", err)
	}
	if refs := pinnedRefs(t, e.parent); refs != "" {
		t.Errorf("pins after purge = %q", refs)
	}
}

// A purge drops the worktree pin after releasing the cleanup lock: update-ref runs the
// repository's reference-transaction hook, and a hook that calls back into the Agent must not
// wait behind the lock the purge holds (a restore holding the deletion gate waits on it too).
func TestWorktreePurgeUnpinsOutsideTheCleanupLock(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not available")
	}
	e := tombstoneSetup(t)
	id := worktreeDeletedWithEntry(t, e)
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sessionx.WithCleanupLock(func() {})
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(probe.Close)
	hooks := filepath.Join(e.home, "hooks")
	answer := filepath.Join(e.home, "hook-answer")
	writeAt(t, filepath.Join(hooks, "reference-transaction"),
		"#!/bin/sh\ncat >/dev/null\ncurl -s -m 3 -o /dev/null -w '%{http_code}' '"+probe.URL+"' >'"+answer+"'\n")
	if err := os.Chmod(filepath.Join(hooks, "reference-transaction"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitAt(t, e.parent, "config", "core.hooksPath", hooks)
	do(t, e.srv, "DELETE", "/cleanup/archives/"+id, nil, http.StatusOK, nil)
	if got, _ := os.ReadFile(answer); string(got) != "204" {
		t.Fatalf("the hook's call back into the Agent answered %q, want 204", got)
	}
	if refs := pinnedRefs(t, e.parent); refs != "" {
		t.Errorf("pins after purge = %q", refs)
	}
}

// unreadableShelvedMeta corrupts aisub01's meta after the delete shelved it and answers the meta
// as it was, its shelf date moved an hour back so it reads as older than any restore attempt.
func unreadableShelvedMeta(t *testing.T) session.Meta {
	t.Helper()
	m, ok := session.ReadMeta("aisub01")
	if !ok || !m.Archived {
		t.Fatalf("aisub01 was not shelved: %+v", m)
	}
	m.ArchivedAt = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if err := os.WriteFile(session.MetaPath("aisub01"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	return m
}

// A shelved session whose meta cannot be read does not hold the folder back: the restore puts
// the worktree and the readable sessions back and reports the unreadable one, and once its meta
// reads again a retry takes it off the shelf without undoing a shelf action taken meanwhile.
func TestWorktreeRestoreWithAnUnreadableShelvedMeta(t *testing.T) {
	e := tombstoneSetup(t)
	id := worktreeDeletedWithEntry(t, e)
	good := unreadableShelvedMeta(t)
	path := "/cleanup/archives/" + id + "/restore"
	if code := errCodeOf(t, e.srv, "POST", path, http.StatusConflict); code != "restore_incomplete" {
		t.Fatalf("restore code = %q, want restore_incomplete", code)
	}
	if got, _ := os.ReadFile(filepath.Join(e.wt, "f")); string(got) != "edited" {
		t.Fatalf("f = %q, want the uncommitted edit back", got)
	}
	if m, ok := session.ReadMeta("aiaaaa1"); !ok || m.Archived {
		t.Fatalf("the readable session was left on the shelf: %+v", m)
	}

	do(t, e.srv, "POST", "/sessions/aiaaaa1/archive", nil, http.StatusOK, nil)
	if err := session.WriteMetaChecked(good); err != nil {
		t.Fatal(err)
	}
	do(t, e.srv, "POST", path, nil, http.StatusOK, nil)
	if m, ok := session.ReadMeta("aisub01"); !ok || m.Archived {
		t.Fatalf("the session readable again was left on the shelf: %+v", m)
	}
	if m, ok := session.ReadMeta("aiaaaa1"); !ok || !m.Archived {
		t.Fatalf("the retry undid the newer shelf action: %+v", m)
	}
}

// A session that could not be read at the first attempt, and that the member shelves again once
// it reads, stays on the shelf: its new shelf date is not before the first attempt.
func TestWorktreeRestoreKeepsAReshelvedOnceUnreadableSession(t *testing.T) {
	e := tombstoneSetup(t)
	id := worktreeDeletedWithEntry(t, e)
	good := unreadableShelvedMeta(t)
	path := "/cleanup/archives/" + id + "/restore"
	if code := errCodeOf(t, e.srv, "POST", path, http.StatusConflict); code != "restore_incomplete" {
		t.Fatalf("restore code = %q, want restore_incomplete", code)
	}
	if err := session.WriteMetaChecked(good); err != nil {
		t.Fatal(err)
	}
	do(t, e.srv, "POST", "/sessions/aisub01/restore", nil, http.StatusOK, nil)
	do(t, e.srv, "POST", "/sessions/aisub01/archive", nil, http.StatusOK, nil)
	do(t, e.srv, "POST", path, nil, http.StatusOK, nil)
	if m, ok := session.ReadMeta("aisub01"); !ok || !m.Archived {
		t.Fatalf("the retry undid the member's shelf action: %+v", m)
	}
}

// A purge of an id whose tarball an earlier purge already removed clears the restore record
// that purge could not.
func TestPurgeClearsARestoreRecordLeftWithoutItsArchive(t *testing.T) {
	e := tombstoneSetup(t)
	id := worktreeDeletedWithEntry(t, e)
	do(t, e.srv, "POST", "/cleanup/archives/"+id+"/restore", nil, http.StatusOK, nil)
	record := filepath.Join(cleanupStoreDir(), worktreeRestoreRecordName(id))
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	do(t, e.srv, "DELETE", "/cleanup/archives/"+id, nil, http.StatusOK, nil)
	writeAt(t, record, string(data)) // as if its unlink had failed
	if code := httpStatus(t, e.srv, "DELETE", "/cleanup/archives/"+id, nil); code == http.StatusOK {
		t.Fatalf("purging a gone archive = %d, want an error", code)
	}
	if _, err := os.Lstat(record); !os.IsNotExist(err) {
		t.Fatalf("the left record is still there: %v", err)
	}
}

// A record a purge left behind under the same id, from another delete, is not this entry's
// progress: its "complete" must not keep this entry's sessions on the shelf.
func TestWorktreeRestoreIgnoresAnotherEntrysRecord(t *testing.T) {
	e := tombstoneSetup(t)
	id := worktreeDeletedWithEntry(t, e)
	stale := `{"id":"` + id + `","ref":"refs/af-deleted/other","shelved":{},"complete":true}`
	writeAt(t, filepath.Join(cleanupStoreDir(), worktreeRestoreRecordName(id)), stale)
	do(t, e.srv, "POST", "/cleanup/archives/"+id+"/restore", nil, http.StatusOK, nil)
	for _, name := range []string{"aiaaaa1", "aisub01"} {
		if m, ok := session.ReadMeta(name); !ok || m.Archived {
			t.Errorf("session %s was left on the shelf: %+v", name, m)
		}
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

// Recreating from the archive on a new branch (the way forward when the trash restore refuses)
// keeps the deleted copy's creator as well.
func TestRecreateOnANewBranchKeepsTheCreator(t *testing.T) {
	e := tombstoneSetup(t)
	if err := gitx.WriteWorktreeCreator(e.wt, "sess-a"); err != nil {
		t.Fatal(err)
	}
	do(t, e.srv, "DELETE", "/repos/app@wt-x?force=true", nil, http.StatusOK, nil)
	gitAt(t, e.parent, "branch", "-q", "-f", "wt-x", "main")
	do(t, e.srv, "POST", "/repos/app@wt-x/recreate",
		map[string]string{"source": gitx.RecreateDeleted, "branch": "wt-x", "new_branch": "wt-y"}, http.StatusCreated, nil)
	if got := gitx.ReadWorktreeCreator(e.wt); got != "sess-a" {
		t.Fatalf("creator after recreate = %q, want sess-a", got)
	}
}

// A copy whose record is unreadable stays unprovable through the trash instead of turning back
// into "never recorded".
func TestUnreadableCreatorRecordSurvivesTheTrash(t *testing.T) {
	e := tombstoneSetup(t)
	if err := gitx.WriteWorktreeCreator(e.wt, "sess-a"); err != nil {
		t.Fatal(err)
	}
	// Truncate the marker the way a torn write would.
	gitDir := strings.TrimSpace(gitAt(t, e.wt, "rev-parse", "--absolute-git-dir"))
	if err := os.WriteFile(filepath.Join(gitDir, "agent-fleet-created-by"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	do(t, e.srv, "DELETE", "/repos/app@wt-x?force=true", nil, http.StatusOK, nil)
	arcs := worktreeArchives(t, e.srv)
	do(t, e.srv, "POST", "/cleanup/archives/"+arcs[0].ID+"/restore", nil, http.StatusOK, nil)
	if s, st := gitx.ReadWorktreeCreatorState(e.wt); s != "" || st != gitx.CreatorStateNone {
		t.Fatalf("restored = %q,%q, want state none", s, st)
	}
}

// A remove that fails part way has already taken files, so the trash entry — the only copy of
// the uncommitted work by then — is kept, pin and all.
func TestPartlyFailedRemoveKeepsTheTrashEntry(t *testing.T) {
	e := tombstoneSetup(t)
	stuck := filepath.Join(e.wt, "stuck")
	if err := os.MkdirAll(stuck, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stuck, "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stuck, 0o555); err != nil { // its file cannot be unlinked
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stuck, 0o755) })
	if code := httpStatus(t, e.srv, "DELETE", "/repos/app@wt-x?force=true", nil); code != http.StatusBadGateway {
		t.Skipf("the remove did not fail (running as root?): %d", code)
	}
	arcs := worktreeArchives(t, e.srv)
	if len(arcs) != 1 || arcs[0].Worktree == nil || arcs[0].Worktree.Snapshot == "" {
		t.Fatalf("trash entry after a partly failed remove = %+v, want it kept with its snapshot", arcs)
	}
	if refs := pinnedRefs(t, e.parent); refs != arcs[0].Worktree.Ref {
		t.Fatalf("pin = %q, want %q kept", refs, arcs[0].Worktree.Ref)
	}
	if got := gitAt(t, e.parent, "show", arcs[0].Worktree.Snapshot+":f"); got != "edited" {
		t.Errorf("snapshot f = %q", got)
	}
}

// A repository git does not track inside the worktree would come back as an empty folder:
// the delete is refused, nothing is recorded, and the worktree is left as it is.
//
// Both shapes: one with a commit (it would become a bare gitlink) and a bare `git init` with none
// (`add -A` cannot even take it, and the refusal must not read as a full disk).
func TestDeleteRefusesAWorktreeWithANestedRepository(t *testing.T) {
	for _, withCommit := range []bool{true, false} {
		name := "no-commit"
		if withCommit {
			name = "with-commit"
		}
		t.Run(name, func(t *testing.T) {
			e := tombstoneSetup(t)
			nested := filepath.Join(e.wt, "vendor", "lib")
			if withCommit {
				gitInit(t, nested)
			} else {
				if err := os.MkdirAll(nested, 0o755); err != nil {
					t.Fatal(err)
				}
				gitAt(t, nested, "init", "-q")
			}
			var res struct {
				Error struct{ Code, Message string }
			}
			do(t, e.srv, "DELETE", "/repos/app@wt-x?force=true", nil, http.StatusConflict, &res)
			if res.Error.Code != errCodeWorktreeNestedRepo || !strings.Contains(res.Error.Message, "vendor/lib") {
				t.Fatalf("refusal = %+v", res.Error)
			}
			if !gitx.IsGitRepo(nested) || !gitx.IsLinkedWorktree(e.wt) {
				t.Fatal("the worktree or its nested repository was touched")
			}
			if refs := pinnedRefs(t, e.parent); refs != "" {
				t.Errorf("a pin was left behind: %q", refs)
			}
			if arcs := worktreeArchives(t, e.srv); len(arcs) != 0 {
				t.Errorf("a trash entry was left behind: %+v", arcs)
			}
		})
	}
}
