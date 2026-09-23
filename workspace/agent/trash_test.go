package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// ADR 0101: every delete of a session goes through the trash, and deleting a working copy
// shelves its stopped AI sessions instead of forgetting them.

// trashHolds reports whether some archive in the trash holds a session named name.
func trashHolds(t *testing.T, name string) bool {
	t.Helper()
	for _, m := range listCleanupArchives() {
		for _, s := range m.Sessions {
			if s.Name == name {
				return true
			}
		}
	}
	return false
}

func trashMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions/{name}/stop", sessionx.HandleStopSession)
	mux.HandleFunc("DELETE /sessions/{name}", handleDeleteSession)
	mux.HandleFunc("DELETE /repos/{name}", gitx.HandleDeleteRepo)
	mux.HandleFunc("POST /cleanup/archives/{id}/restore", handleRestoreCleanupArchive)
	return mux
}

// TestEveryDeleteRouteGoesThroughTheTrash: /stop, DELETE and DELETE ?reclaim=1 each archive
// the meta and the transcript before removing them, and the trash brings both back. /stop and
// the reclaim-less DELETE used to forget the meta and leave the transcript behind.
func TestEveryDeleteRouteGoesThroughTheTrash(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	srv := httptest.NewServer(trashMux())
	defer srv.Close()
	dir := filepath.Join(home, "repos", "app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	for _, route := range []struct{ name, method, path string }{
		{"stopx01", "POST", "/sessions/stopx01/stop"},
		{"delx001", "DELETE", "/sessions/delx001"},
		{"recl001", "DELETE", "/sessions/recl001?reclaim=1"},
	} {
		t.Run(route.name, func(t *testing.T) {
			m := session.Meta{Name: route.name, Dir: dir, Kind: session.KindClaude, CreatedAt: time.Now().Format(time.RFC3339)}
			session.WriteMeta(m)
			jsonl := filepath.Join(home, "claude", "projects", "-app", session.UUID(dir, m.Name)+".jsonl")
			if err := os.MkdirAll(filepath.Dir(jsonl), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(jsonl, []byte(`{"type":"user"}`+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			var out struct{ Archive string }
			do(t, srv, route.method, route.path, nil, http.StatusOK, &out)
			if out.Archive == "" {
				t.Fatalf("%s %s answered without an archive id", route.method, route.path)
			}
			if _, ok := session.ReadMeta(m.Name); ok {
				t.Fatal("meta still there after the delete")
			}
			if _, err := os.Stat(jsonl); err == nil {
				t.Fatal("transcript left on disk after the delete (the old /stop leak)")
			}
			if !trashHolds(t, m.Name) {
				t.Fatal("the deleted session is not in the trash")
			}

			do(t, srv, "POST", "/cleanup/archives/"+out.Archive+"/restore", nil, http.StatusOK, nil)
			if _, ok := session.ReadMeta(m.Name); !ok {
				t.Fatal("restore did not bring the meta back")
			}
			if _, err := os.Stat(jsonl); err != nil {
				t.Fatalf("restore did not bring the transcript back: %v", err)
			}
		})
	}
}

// TestTrashRemovesNothingWhenTheArchiveFails: if the gz cannot be written, the session stays
// exactly as it was. A delete that removed first and archived second would lose it.
func TestTrashRemovesNothingWhenTheArchiveFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	srv := httptest.NewServer(trashMux())
	defer srv.Close()
	// The trash directory is a file, so no archive can be written into it.
	store := cleanupStoreDir()
	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "keep001", Dir: home, Kind: session.KindShell})

	for _, path := range []string{"/sessions/keep001/stop", "/sessions/keep001"} {
		method := "DELETE"
		if filepath.Base(path) == "stop" {
			method = "POST"
		}
		if code := httpStatus(t, srv, method, path, nil); code != http.StatusInternalServerError {
			t.Fatalf("%s %s with an unwritable trash = %d, want 500", method, path, code)
		}
		if _, ok := session.ReadMeta("keep001"); !ok {
			t.Fatalf("%s %s removed the meta although nothing reached the trash", method, path)
		}
	}
}

// TestDeletingAWorktreeShelvesItsSessions: deleting a working copy is a decision about the
// files. A stopped AI session moves to the shelf, a stopped shell moves to the trash, an
// archived one is left alone — with or without ?prune_sessions=1, which used to decide whether
// the metas were forgotten (and forgot them without the trash).
func TestDeletingAWorktreeShelvesItsSessions(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	for _, query := range []string{"", "?prune_sessions=1"} {
		t.Run("query="+query, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
			srv := httptest.NewServer(trashMux())
			defer srv.Close()

			parent := filepath.Join(home, "repos", "app")
			gitInit(t, parent)
			wt, err := gitx.EnsureWorktree(parent, "main", "wt-x", "")
			if err != nil {
				t.Fatal(err)
			}
			session.WriteMeta(session.Meta{Name: "aiaaaa1", Dir: wt, Kind: session.KindClaude})
			session.WriteMeta(session.Meta{Name: "shbbbb1", Dir: filepath.Join(wt, "sub"), Kind: session.KindShell})
			session.WriteMeta(session.Meta{Name: "arcccc1", Dir: wt, Kind: session.KindClaude, Archived: true})
			session.WriteMeta(session.Meta{Name: "elsddd1", Dir: parent, Kind: session.KindShell})

			do(t, srv, "DELETE", "/repos/"+filepath.Base(wt)+query, nil, http.StatusOK, nil)
			if gitx.IsGitRepo(wt) {
				t.Fatal("the worktree is still there")
			}
			if m, ok := session.ReadMeta("aiaaaa1"); !ok || !m.Archived {
				t.Fatalf("stopped AI session: meta=%+v ok=%v, want it on the shelf", m, ok)
			}
			if trashHolds(t, "aiaaaa1") {
				t.Fatal("the stopped AI session went to the trash; it belongs on the shelf")
			}
			if _, ok := session.ReadMeta("shbbbb1"); ok || !trashHolds(t, "shbbbb1") {
				t.Fatalf("stopped shell: meta still listed=%v, in trash=%v — want it in the trash", ok, trashHolds(t, "shbbbb1"))
			}
			if m, ok := session.ReadMeta("arcccc1"); !ok || !m.Archived {
				t.Fatalf("shelved session was touched: meta=%+v ok=%v", m, ok)
			}
			if _, ok := session.ReadMeta("elsddd1"); !ok {
				t.Fatal("a session outside the deleted worktree was touched")
			}
		})
	}
}

// Review 115 🔴1: two deletes of the same session at once. One wins, the other answers 404, and
// the winner's archive — the only copy — survives. Before, both wrote the same second-precise id
// (the second overwrote the first) and the loser purged it.
func TestConcurrentDeletesKeepTheOnlyArchive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	srv := httptest.NewServer(trashMux())
	defer srv.Close()
	m := session.Meta{Name: "twice01", Dir: home, Kind: session.KindClaude}
	session.WriteMeta(m)
	jsonl := filepath.Join(home, "claude", "projects", "-h", session.UUID(home, m.Name)+".jsonl")
	if err := os.MkdirAll(filepath.Dir(jsonl), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonl, []byte(`{"type":"user"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() { codes <- httpStatus(t, srv, "DELETE", "/sessions/twice01", nil) }()
	}
	got := map[int]int{}
	got[<-codes]++
	got[<-codes]++
	if got[http.StatusOK] != 1 || got[http.StatusNotFound] != 1 {
		t.Fatalf("two concurrent deletes answered %v, want one 200 and one 404", got)
	}
	if !trashHolds(t, "twice01") {
		t.Fatal("no archive left for the deleted session")
	}
}

// Two archives written with the same id never overwrite each other.
func TestArchiveIDsNeverCollide(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	at := time.Date(2026, 9, 24, 1, 0, 0, 0, time.UTC)
	a := cleanupManifest{ID: newCleanupID(at, "same"), At: at.Format(time.RFC3339)}
	b := a
	if err := writeCleanupArchive(&a, nil); err != nil {
		t.Fatal(err)
	}
	if err := writeCleanupArchive(&b, nil); err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || len(listCleanupArchives()) != 2 {
		t.Fatalf("ids %q / %q, %d archives — want two distinct archives", a.ID, b.ID, len(listCleanupArchives()))
	}
}

// Review 115 🔴2: the session was resumed while its archive was being written (its transcript
// grew). Nothing is removed, the stale archive goes, and the caller hears 409.
func TestTrashAbortsWhenTheTranscriptGrewMeanwhile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	srv := httptest.NewServer(trashMux())
	defer srv.Close()
	m := session.Meta{Name: "grew001", Dir: home, Kind: session.KindClaude}
	session.WriteMeta(m)
	jsonl := filepath.Join(home, "claude", "projects", "-h", session.UUID(home, m.Name)+".jsonl")
	if err := os.MkdirAll(filepath.Dir(jsonl), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonl, []byte(`{"type":"user"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	trashAfterArchive = func(session.Meta) {
		f, _ := os.OpenFile(jsonl, os.O_APPEND|os.O_WRONLY, 0o600)
		_, _ = f.WriteString(`{"type":"assistant"}` + "\n")
		_ = f.Close()
	}
	defer func() { trashAfterArchive = nil }()

	if code := httpStatus(t, srv, "DELETE", "/sessions/grew001", nil); code != http.StatusConflict {
		t.Fatalf("delete while the transcript grew = %d, want 409", code)
	}
	if _, ok := session.ReadMeta("grew001"); !ok {
		t.Fatal("meta removed although the conversation moved on")
	}
	if b, _ := os.ReadFile(jsonl); len(b) == 0 {
		t.Fatal("transcript removed although the conversation moved on")
	}
	if trashHolds(t, "grew001") {
		t.Fatal("the stale archive was left in the trash")
	}
}

// Review 115 🟡4: when a shell of the working copy cannot go to the trash, the working copy is
// not deleted either (it used to be deleted, answer 200, and leave the shell listed with no
// folder).
func TestWorktreeStaysWhenItsShellCannotBeTrashed(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	srv := httptest.NewServer(trashMux())
	defer srv.Close()
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent)
	wt, err := gitx.EnsureWorktree(parent, "main", "wt-y", "")
	if err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "shfull1", Dir: wt, Kind: session.KindShell})
	store := cleanupStoreDir() // a file where the trash directory should be: every archive fails
	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := httpStatus(t, srv, "DELETE", "/repos/"+filepath.Base(wt), nil); code != http.StatusInternalServerError {
		t.Fatalf("delete with an unwritable trash = %d, want 500", code)
	}
	if !gitx.IsGitRepo(wt) {
		t.Fatal("the working copy was deleted although its shell could not go to the trash")
	}
	if _, ok := session.ReadMeta("shfull1"); !ok {
		t.Fatal("the shell's meta is gone")
	}
}

// Review 115 🔴3: a lock request waits for a working-copy delete in flight instead of being
// answered and then deleted anyway.
func TestLockWaitsForAWorkingCopyDelete(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions/{name}/lock", sessionx.HandleSessionLock)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	session.WriteMeta(session.Meta{Name: "gate001", Dir: home, Kind: session.KindShell})

	inGate, release := make(chan struct{}), make(chan struct{})
	go sessionx.WithDeletionGate(func() { close(inGate); <-release })
	<-inGate
	done := make(chan int, 1)
	go func() { done <- httpStatus(t, srv, "POST", "/sessions/gate001/lock", map[string]any{"locked": true}) }()
	select {
	case <-done:
		t.Fatal("the lock was answered while a working-copy delete held the gate")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("lock after the delete = %d, want 200", code)
	}
}

// Review 115 🟡7: purging an old archive keeps the managed ledger while another archive still
// holds the same session (deleted, restored, deleted again) — restoring that one must find it.
func TestPurgeKeepsTheLedgerAnotherArchiveNeeds(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	m := session.Meta{Name: "ledg001", Dir: home, Kind: session.KindCodex}
	ledgerFile := filepath.Join(paths.AgentStateDir(), "codex-msgledger", m.Name+".json")
	if err := os.MkdirAll(filepath.Dir(ledgerFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledgerFile, []byte(`["m1"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	mk := func(sec int) string {
		at := time.Date(2026, 8, 1, 0, 0, sec, 0, time.UTC)
		man := cleanupManifest{ID: newCleanupID(at, "ledg001"), At: at.Format(time.RFC3339), Reason: "delete_session",
			Sessions: []cleanupArchivedSession{{Name: m.Name, Kind: m.Kind, Meta: marshalMeta(m)}}}
		if err := writeCleanupArchive(&man, nil); err != nil {
			t.Fatal(err)
		}
		return man.ID
	}
	older, newer := mk(0), mk(1)

	man, err := purgeCleanupArchive(older)
	if err != nil {
		t.Fatal(err)
	}
	dropPurgedLedgers([]cleanupManifest{man})
	if _, err := os.Stat(ledgerFile); err != nil {
		t.Fatal("the ledger went although another archive still holds the session")
	}
	man, err = purgeCleanupArchive(newer)
	if err != nil {
		t.Fatal(err)
	}
	dropPurgedLedgers([]cleanupManifest{man})
	if _, err := os.Stat(ledgerFile); err == nil {
		t.Fatal("the ledger stayed after the last archive holding the session was purged")
	}
}
