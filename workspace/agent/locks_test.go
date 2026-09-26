package main

import (
	"encoding/json"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// lockMux wires the routes the delete lock (docs/log/45) governs, so each test drives
// the real handlers over HTTP rather than calling internals.
func lockMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sessions", sessionx.HandleListSessions)
	mux.HandleFunc("GET /sessions/archived", sessionx.HandleListArchived)
	mux.HandleFunc("POST /sessions/{name}/lock", sessionx.HandleSessionLock)
	mux.HandleFunc("POST /sessions/{name}/stop", sessionx.HandleStopSession)
	mux.HandleFunc("POST /sessions/{name}/archive", sessionx.HandleArchiveSession)
	mux.HandleFunc("DELETE /sessions/{name}", handleDeleteSession)
	mux.HandleFunc("GET /repos", gitx.HandleListRepos)
	mux.HandleFunc("POST /repos/{name}/lock", sessionx.HandleRepoLock)
	mux.HandleFunc("DELETE /repos/{name}", gitx.HandleDeleteRepo)
	mux.HandleFunc("POST /chat/conversations/{id}/lock", sessionx.HandleChatLock)
	mux.HandleFunc("DELETE /chat/conversations/{id}", chatx.HandleChatDelete)
	return mux
}

// TestSessionLockRefusesDeletion: a locked session survives BOTH delete routes — /stop (the
// old name of the Console's Delete) and DELETE — while archive (reversible) still works.
// Unlocking restores deletability. Both routes end in the trash (ADR 0101).
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

// A GET /sessions list has a small side effect: it stamps a stopped session's StoppedAt. It
// works from a ListMetas snapshot that can predate a concurrent lock toggle, so that
// bookkeeping is written onto the meta as it is on disk (issue #950) and must not write
// Locked=false back over the newly saved lock.
func TestListMetaWriteKeepsNewerSessionLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	dir := filepath.Join(home, "repos", "app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "slot01", Dir: dir, Kind: session.KindShell, Locked: true})
	srv := httptest.NewServer(lockMux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	got, ok := session.ReadMeta("slot01")
	if !ok || !got.Locked || got.StoppedAt == "" {
		t.Fatalf("after the list stamped the stop: meta=%+v ok=%v, want StoppedAt written and still locked", got, ok)
	}
}

// TestSessionLockSurvivesTTLSweep: the TTL sweep archives rather than deletes (ADR 0097),
// so this asserts both halves of that — the unlocked twin leaves the active list with its
// meta and transcript intact, and the locked row is exempt from the sweep entirely because
// a pinned session is one the user wants to keep seeing.
func TestSessionLockSurvivesTTLSweep(t *testing.T) {
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
		t.Error("locked session was swept out of the active list")
	}
	if seen["dropme"] {
		t.Error("unlocked stale session should have left the active list")
	}
	if m, ok := session.ReadMeta("keepme"); !ok || m.Archived {
		t.Errorf("locked meta was archived or deleted by the TTL sweep: %+v ok=%v", m, ok)
	}
	// The sweep MOVES it: the meta survives, marked archived, so the conversation is still
	// restorable. Deleting here would be the only unrecoverable removal nobody asked for.
	m, ok := session.ReadMeta("dropme")
	if !ok {
		t.Fatal("swept meta was deleted from disk instead of archived")
	}
	if !m.Archived {
		t.Error("swept meta is on disk but not marked archived")
	}
	var shelf struct {
		Sessions []session.Session `json:"sessions"`
	}
	do(t, srv, "GET", "/sessions/archived", nil, http.StatusOK, &shelf)
	if len(shelf.Sessions) != 1 || shelf.Sessions[0].Name != "dropme" {
		t.Errorf("shelf = %+v, want the swept session listed for restore", shelf.Sessions)
	}
}

// TestTTLSweepFollowsTheUsersSetting: the archive period is the user's setting (Settings >
// Agents > Session) ahead of AF_SESSION_STOPPED_TTL, read on the list itself, so a change needs
// no restart. The env var is set to 1s throughout: a row that survives it survived because the
// setting won. The locked twin is exempt whatever the period.
func TestTTLSweepFollowsTheUsersSetting(t *testing.T) {
	for _, tc := range []struct {
		name     string
		days     int
		archived bool // the unlocked row, stopped two days ago
	}{
		{"setting longer than the stop beats the env var", 3, false},
		{"setting shorter than the stop archives", 1, true},
		{"off never archives", session.StoppedArchiveNever, false},
		{"a value the Console cannot produce falls back to the env var", 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
			t.Setenv("AF_SESSION_STOPPED_TTL", "1s")
			if err := os.MkdirAll(filepath.Dir(uiprefs.Path()), 0o700); err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(map[string]any{"sessionStoppedArchiveDays": tc.days})
			if err := os.WriteFile(uiprefs.Path(), b, 0o600); err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(lockMux())
			defer srv.Close()

			dir := filepath.Join(home, "repos", "app")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			stopped := time.Now().Add(-48 * time.Hour).Format(time.RFC3339)
			session.WriteMeta(session.Meta{Name: "keepme", Dir: dir, Kind: session.KindShell, StoppedAt: stopped, Locked: true})
			session.WriteMeta(session.Meta{Name: "stale", Dir: dir, Kind: session.KindShell, StoppedAt: stopped})

			do(t, srv, "GET", "/sessions", nil, http.StatusOK, nil)
			if m, ok := session.ReadMeta("stale"); !ok || m.Archived != tc.archived {
				t.Errorf("stale: archived=%v ok=%v, want archived=%v", m.Archived, ok, tc.archived)
			}
			if m, ok := session.ReadMeta("keepme"); !ok || m.Archived {
				t.Errorf("locked row was archived: %+v ok=%v", m, ok)
			}
		})
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
	var list struct{ Repos []gitx.Repo }
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

// TestChatLockRefusesDelete: a locked assistant conversation refuses DELETE, and its
// list entry carries the flag.
func TestChatLockRefusesDelete(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srv := httptest.NewServer(lockMux())
	defer srv.Close()

	c := &chatx.ChatConversation{ID: chatx.RandUUID(), Title: "残したい会話", CreatedAt: chatx.NowMs(), UpdatedAt: chatx.NowMs()}
	if err := chatx.SaveConv(c); err != nil {
		t.Fatal(err)
	}

	do(t, srv, "POST", "/chat/conversations/"+c.ID+"/lock", map[string]any{"locked": true}, http.StatusOK, nil)
	metas, err := chatx.ListConvs()
	if err != nil || len(metas) != 1 || !metas[0].Locked {
		t.Fatalf("conversation list = %+v (err=%v), want locked=true", metas, err)
	}
	if code := httpStatus(t, srv, "DELETE", "/chat/conversations/"+c.ID, nil); code != http.StatusForbidden {
		t.Fatalf("delete locked conversation = %d, want 403", code)
	}
	if _, err := chatx.LoadConv(c.ID); err != nil {
		t.Fatalf("locked conversation was deleted: %v", err)
	}
	do(t, srv, "POST", "/chat/conversations/"+c.ID+"/lock", map[string]any{"locked": false}, http.StatusOK, nil)
	if code := httpStatus(t, srv, "DELETE", "/chat/conversations/"+c.ID, nil); code != http.StatusOK {
		t.Fatalf("delete after unlock = %d, want 200", code)
	}
	if _, err := chatx.LoadConv(c.ID); err == nil {
		t.Fatal("conversation should be gone after unlock+delete")
	}
}
