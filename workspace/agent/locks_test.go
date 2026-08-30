package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// lockMux wires the routes the delete lock (docs/log/45) governs, so each test drives
// the real handlers over HTTP rather than calling internals.
func lockMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sessions", handleListSessions)
	mux.HandleFunc("POST /sessions/{name}/lock", handleSessionLock)
	mux.HandleFunc("POST /sessions/{name}/stop", handleStopSession)
	mux.HandleFunc("POST /sessions/{name}/archive", handleArchiveSession)
	mux.HandleFunc("DELETE /sessions/{name}", handleDeleteSession)
	mux.HandleFunc("GET /repos", handleListRepos)
	mux.HandleFunc("POST /repos/{name}/lock", handleRepoLock)
	mux.HandleFunc("DELETE /repos/{name}", handleDeleteRepo)
	mux.HandleFunc("POST /chat/conversations/{id}/lock", handleChatLock)
	mux.HandleFunc("DELETE /chat/conversations/{id}", handleChatDelete)
	return mux
}

// TestSessionLockRefusesDeletion: a locked session survives BOTH manual delete paths
// — /stop (the Console's 削除, which forgets the meta) and DELETE ?reclaim=1 (jsonl
// reclaim) — while archive (reversible) still works. Unlocking restores deletability.
func TestSessionLockRefusesDeletion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	srv := httptest.NewServer(lockMux())
	defer srv.Close()

	dir := filepath.Join(home, "repos", "app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "slot01", Dir: dir, Kind: session.KindShell, CreatedAt: time.Now().Format(time.RFC3339)})

	// Lock, then try both delete routes.
	do(t, srv, "POST", "/sessions/slot01/lock", map[string]any{"locked": true}, http.StatusOK, nil)
	if code := httpStatus(t, srv, "POST", "/sessions/slot01/stop", nil); code != http.StatusForbidden {
		t.Fatalf("stop on locked session = %d, want 403", code)
	}
	if code := httpStatus(t, srv, "DELETE", "/sessions/slot01?reclaim=1", nil); code != http.StatusForbidden {
		t.Fatalf("delete on locked session = %d, want 403", code)
	}
	if _, ok := session.ReadMeta("slot01"); !ok {
		t.Fatal("locked session meta was removed")
	}
	// Archive is reversible, so the lock does not block it (the row is restorable).
	do(t, srv, "POST", "/sessions/slot01/archive", nil, http.StatusOK, nil)
	if m, ok := session.ReadMeta("slot01"); !ok || !m.Archived || !m.Locked {
		t.Fatalf("after archive: meta=%+v ok=%v — want archived, still locked", m, ok)
	}

	// Unlock → the same delete now goes through.
	do(t, srv, "POST", "/sessions/slot01/lock", map[string]any{"locked": false}, http.StatusOK, nil)
	if code := httpStatus(t, srv, "DELETE", "/sessions/slot01?reclaim=1", nil); code != http.StatusOK {
		t.Fatalf("delete after unlock = %d, want 200", code)
	}
	if _, ok := session.ReadMeta("slot01"); ok {
		t.Fatal("meta should be gone after unlock+delete")
	}
}

// A GET /sessions list has a small side effect: it stamps a stopped session's
// StoppedAt. Its meta snapshot can predate a concurrent lock toggle, so that
// bookkeeping must not write Locked=false back over the newly saved lock.
func TestListMetaWriteKeepsNewerSessionLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	dir := filepath.Join(home, "repos", "app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := session.Meta{Name: "slot01", Dir: dir, Kind: session.KindShell}
	session.WriteMeta(stale)

	// Simulate POST /lock completing after GET /sessions took its snapshot.
	fresh, ok := session.ReadMeta("slot01")
	if !ok {
		t.Fatal("session meta missing")
	}
	fresh.Locked = true
	session.WriteMeta(fresh)
	stale.StoppedAt = time.Now().Format(time.RFC3339)
	writeSessionMetaKeepingLock(stale)

	got, ok := session.ReadMeta("slot01")
	if !ok || !got.Locked {
		t.Fatalf("list bookkeeping cleared a newer lock: meta=%+v ok=%v", got, ok)
	}
}

// TestSessionLockSurvivesTTLPrune: the 7-day auto-prune of stopped sessions is a
// deletion too — a locked row must stay listed past its TTL while its unlocked twin
// is pruned away.
func TestSessionLockSurvivesTTLPrune(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	t.Setenv("AF_SESSION_STOPPED_TTL", "1s")
	srv := httptest.NewServer(lockMux())
	defer srv.Close()

	dir := filepath.Join(home, "repos", "app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour).Format(time.RFC3339)
	session.WriteMeta(session.Meta{Name: "keepme", Dir: dir, Kind: session.KindShell, StoppedAt: old, Locked: true})
	session.WriteMeta(session.Meta{Name: "dropme", Dir: dir, Kind: session.KindShell, StoppedAt: old})

	var list struct {
		Sessions []session.Session `json:"sessions"`
	}
	do(t, srv, "GET", "/sessions", nil, http.StatusOK, &list)
	seen := map[string]bool{}
	for _, s := range list.Sessions {
		seen[s.Name] = true
		if s.Name == "keepme" && !s.Locked {
			t.Error("wire session lost the locked flag")
		}
	}
	if !seen["keepme"] {
		t.Error("locked session was pruned by the TTL sweep")
	}
	if seen["dropme"] {
		t.Error("unlocked stale session should have been pruned")
	}
	if _, ok := session.ReadMeta("keepme"); !ok {
		t.Error("locked meta was deleted from disk by the TTL sweep")
	}
}

// TestRepoLockRefusesDelete: a locked working copy refuses DELETE even with
// force=true (the lock is the one guard force cannot override), and the repo list
// carries the flag so the Console can badge it.
func TestRepoLockRefusesDelete(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	srv := httptest.NewServer(lockMux())
	defer srv.Close()

	dir := filepath.Join(home, "repos", "app")
	gitInit(t, dir)

	do(t, srv, "POST", "/repos/app/lock", map[string]any{"locked": true}, http.StatusOK, nil)
	var list struct{ Repos []Repo }
	do(t, srv, "GET", "/repos", nil, http.StatusOK, &list)
	if len(list.Repos) != 1 || !list.Repos[0].Locked {
		t.Fatalf("repo list = %+v, want locked=true", list.Repos)
	}
	if code := httpStatus(t, srv, "DELETE", "/repos/app", nil); code != http.StatusForbidden {
		t.Fatalf("delete locked working copy = %d, want 403", code)
	}
	if code := httpStatus(t, srv, "DELETE", "/repos/app?force=true", nil); code != http.StatusForbidden {
		t.Fatalf("force-delete locked working copy = %d, want 403", code)
	}
	if !session.DirExists(dir) {
		t.Fatal("locked working copy was removed")
	}
	do(t, srv, "POST", "/repos/app/lock", map[string]any{"locked": false}, http.StatusOK, nil)
	if code := httpStatus(t, srv, "DELETE", "/repos/app", nil); code != http.StatusOK {
		t.Fatalf("delete after unlock = %d, want 200", code)
	}
}

// TestRepoDeleteRefusedByLockedSession: deleting a working copy that hosts a LOCKED
// session would strand it (its dir vanishes, resume gone), so that delete is refused
// too — the lock protects the session's ability to come back, not just its meta row.
func TestRepoDeleteRefusedByLockedSession(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	srv := httptest.NewServer(lockMux())
	defer srv.Close()

	dir := filepath.Join(home, "repos", "app")
	gitInit(t, dir)
	session.WriteMeta(session.Meta{Name: "slot01", Dir: dir, Kind: session.KindShell, Locked: true})

	code, raw := roundtrip(t, srv, "DELETE", "/repos/app", nil)
	if code != http.StatusForbidden {
		t.Fatalf("delete working copy of a locked session = %d (%s), want 403", code, raw)
	}
	var errBody struct {
		Error struct{ Code string } `json:"error"`
	}
	if json.Unmarshal(raw, &errBody) == nil && errBody.Error.Code != errCodeLockedSessions {
		t.Errorf("error code = %q, want %q", errBody.Error.Code, errCodeLockedSessions)
	}
	if !session.DirExists(dir) {
		t.Fatal("working copy was removed despite a locked session living in it")
	}
}

// TestWorktreeLockBlocksAutoPrune: maybePruneWorktree drops a clean, session-less
// worktree on its own (no user action) — the lock must stop that automatic path too.
func TestWorktreeLockBlocksAutoPrune(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))

	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent)
	wt := filepath.Join(home, "repos", "app@wt")
	cmd := exec.Command("git", "-C", parent, "worktree", "add", "-b", "wt", wt)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v: %s", err, out)
	}

	if err := setRepoLock(wt, true); err != nil {
		t.Fatal(err)
	}
	maybePruneWorktree(wt)
	if !session.DirExists(wt) {
		t.Fatal("locked worktree was auto-pruned")
	}
	// Same call once unlocked removes it — proving the test's prune really would fire.
	if err := setRepoLock(wt, false); err != nil {
		t.Fatal(err)
	}
	maybePruneWorktree(wt)
	if session.DirExists(wt) {
		t.Fatal("unlocked clean worktree should have been pruned")
	}
}

// TestChatLockRefusesDelete: a locked assistant conversation refuses DELETE, and its
// list entry carries the flag.
func TestChatLockRefusesDelete(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srv := httptest.NewServer(lockMux())
	defer srv.Close()

	c := &chatConversation{ID: randUUID(), Title: "残したい会話", CreatedAt: nowMs(), UpdatedAt: nowMs()}
	if err := saveConv(c); err != nil {
		t.Fatal(err)
	}

	do(t, srv, "POST", "/chat/conversations/"+c.ID+"/lock", map[string]any{"locked": true}, http.StatusOK, nil)
	metas, err := listConvs()
	if err != nil || len(metas) != 1 || !metas[0].Locked {
		t.Fatalf("conversation list = %+v (err=%v), want locked=true", metas, err)
	}
	if code := httpStatus(t, srv, "DELETE", "/chat/conversations/"+c.ID, nil); code != http.StatusForbidden {
		t.Fatalf("delete locked conversation = %d, want 403", code)
	}
	if _, err := loadConv(c.ID); err != nil {
		t.Fatalf("locked conversation was deleted: %v", err)
	}
	do(t, srv, "POST", "/chat/conversations/"+c.ID+"/lock", map[string]any{"locked": false}, http.StatusOK, nil)
	if code := httpStatus(t, srv, "DELETE", "/chat/conversations/"+c.ID, nil); code != http.StatusOK {
		t.Fatalf("delete after unlock = %d, want 200", code)
	}
	if _, err := loadConv(c.ID); err == nil {
		t.Fatal("conversation should be gone after unlock+delete")
	}
}
