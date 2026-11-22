package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
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
	// A worktree with no commit yet: a delete records no tombstone for it, so the unwritable
	// trash is met by the shell's archive, which is the step under test (a worktree with a
	// commit is refused one step earlier — TestWorktreeStaysWhenItsTombstoneCannotBeWritten).
	wt := filepath.Join(home, "repos", "app@wt-y")
	gitAt(t, parent, "worktree", "add", "-q", "--orphan", "-b", "wt-y", wt)
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
	releaseOnce(release)
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

// The Agent's backup of a session's transcript (claude.BackupTranscript) is removed when the
// session is deleted, and a session whose own log was already gone does not lose its
// conversation in the same step: the cleanup archive carries the backup, and restoring it puts
// the log back where claude looks for it.
func TestTrashRemovesTranscriptBackupAndArchivesItWhenLogIsGone(t *testing.T) {
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
	backupRoot := filepath.Join(paths.AgentDataDir(), "claude-transcripts")

	for _, tc := range []struct {
		name    string
		logGone bool
	}{{"bkplive", false}, {"bkpgone", true}} {
		t.Run(tc.name, func(t *testing.T) {
			m := session.Meta{Name: tc.name, Dir: dir, Kind: session.KindClaude, CreatedAt: time.Now().Format(time.RFC3339)}
			session.WriteMeta(m)
			sid := session.UUID(dir, m.Name)
			jsonl := filepath.Join(home, "claude", "projects", claude.ProjectKey(dir), sid+".jsonl")
			if err := os.MkdirAll(filepath.Dir(jsonl), 0o700); err != nil {
				t.Fatal(err)
			}
			body := `{"type":"user","message":"keep me"}` + "\n"
			if err := os.WriteFile(jsonl, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := claude.BackupTranscript(m); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(backupRoot, sid)); err != nil {
				t.Fatalf("no backup was made: %v", err)
			}
			if tc.logGone {
				if err := os.Remove(jsonl); err != nil {
					t.Fatal(err)
				}
			}

			var out struct{ Archive string }
			do(t, srv, "DELETE", "/sessions/"+tc.name, nil, http.StatusOK, &out)
			if left, _ := filepath.Glob(filepath.Join(backupRoot, sid+"*")); len(left) != 0 {
				t.Fatalf("backup left after the delete: %v", left)
			}
			do(t, srv, "POST", "/cleanup/archives/"+out.Archive+"/restore", nil, http.StatusOK, nil)
			got, err := os.ReadFile(jsonl)
			if err != nil || string(got) != body {
				t.Fatalf("restored transcript = %q, %v; want the conversation back", got, err)
			}
		})
	}
}

// A stub where the current log was must not stand in for it: the archive takes the good backup,
// and restoring the archive gives the conversation back, not the stub.
func TestTrashArchivesBackupWhenLogIsReplacedByStub(t *testing.T) {
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
	m := session.Meta{Name: "bkpstub", Dir: dir, Kind: session.KindClaude, CreatedAt: time.Now().Format(time.RFC3339)}
	session.WriteMeta(m)
	jsonl := filepath.Join(home, "claude", "projects", claude.ProjectKey(dir), session.UUID(dir, m.Name)+".jsonl")
	if err := os.MkdirAll(filepath.Dir(jsonl), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"user","message":"keep me"}` + "\n"
	if err := os.WriteFile(jsonl, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := claude.BackupTranscript(m); err != nil {
		t.Fatal(err)
	}
	// Not even a complete line: the substring "user" is present, a conversation is not.
	if err := os.WriteFile(jsonl, []byte(`{"type":"user","message":"INCOMPLETE`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out struct{ Archive string }
	do(t, srv, "DELETE", "/sessions/bkpstub", nil, http.StatusOK, &out)
	_ = os.Remove(jsonl)
	do(t, srv, "POST", "/cleanup/archives/"+out.Archive+"/restore", nil, http.StatusOK, nil)
	if got, err := os.ReadFile(jsonl); err != nil || string(got) != body {
		t.Fatalf("restored = %q, %v; want the backed-up conversation", got, err)
	}
}

// "A good log exists somewhere" is not a reason to drop the backup: an older complete log in
// another project, or an older complete log at the canonical path, must not displace a newer
// backup in the archive.
func TestTrashKeepsNewerBackupAgainstOlderCompleteLogs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		other bool // the older log sits in another project, with a partial one at the canonical path
	}{{"bkpoldother", true}, {"bkpoldlive", false}} {
		t.Run(tc.name, func(t *testing.T) {
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
			m := session.Meta{Name: tc.name, Dir: dir, Kind: session.KindClaude, CreatedAt: time.Now().Format(time.RFC3339)}
			session.WriteMeta(m)
			id := session.UUID(dir, m.Name)
			canon := filepath.Join(home, "claude", "projects", claude.ProjectKey(dir), id+".jsonl")
			if err := os.MkdirAll(filepath.Dir(canon), 0o700); err != nil {
				t.Fatal(err)
			}
			newest := `{"type":"user","message":"NEWEST"}` + "\n"
			older := `{"type":"user","message":"OLDER"}` + "\n"
			if err := os.WriteFile(canon, []byte(newest), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := claude.BackupTranscript(m); err != nil {
				t.Fatal(err)
			}
			oldPath := canon
			if tc.other {
				if err := os.WriteFile(canon, []byte(`{"type":"user","message":"INCOMPLETE`), 0o600); err != nil {
					t.Fatal(err)
				}
				oldPath = filepath.Join(home, "claude", "projects", "elsewhere", id+".jsonl")
				if err := os.MkdirAll(filepath.Dir(oldPath), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(oldPath, []byte(older), 0o600); err != nil {
				t.Fatal(err)
			}
			past := time.Now().Add(-24 * time.Hour)
			if err := os.Chtimes(oldPath, past, past); err != nil {
				t.Fatal(err)
			}

			var out struct{ Archive string }
			do(t, srv, "DELETE", "/sessions/"+tc.name, nil, http.StatusOK, &out)
			_ = os.RemoveAll(filepath.Join(home, "claude", "projects"))
			do(t, srv, "POST", "/cleanup/archives/"+out.Archive+"/restore", nil, http.StatusOK, nil)
			if got, err := os.ReadFile(canon); err != nil || string(got) != newest {
				t.Fatalf("restored canonical log = %q, %v; want the newer backup", got, err)
			}
		})
	}
}

// A complete live log newer than the backup takes the canonical path in the archive; the backup
// is still archived (beside it), so nothing depends on a check made before the read.
func TestTrashNewerLiveLogWinsAndBackupStillArchived(t *testing.T) {
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
	m := session.Meta{Name: "bkplivewin", Dir: dir, Kind: session.KindClaude, CreatedAt: time.Now().Format(time.RFC3339)}
	session.WriteMeta(m)
	canon := filepath.Join(home, "claude", "projects", claude.ProjectKey(dir), session.UUID(dir, m.Name)+".jsonl")
	if err := os.MkdirAll(filepath.Dir(canon), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canon, []byte(`{"type":"user","m":"OLD"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := claude.BackupTranscript(m); err != nil {
		t.Fatal(err)
	}
	newer := `{"type":"user","m":"NEW"}` + "\n"
	if err := os.WriteFile(canon, []byte(newer), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(canon, future, future); err != nil {
		t.Fatal(err)
	}
	var out struct{ Archive string }
	do(t, srv, "DELETE", "/sessions/bkplivewin", nil, http.StatusOK, &out)
	do(t, srv, "POST", "/cleanup/archives/"+out.Archive+"/restore", nil, http.StatusOK, nil)
	if got, _ := os.ReadFile(canon); string(got) != newer {
		t.Fatalf("canonical = %q, want the newer live log", got)
	}
	if got, err := os.ReadFile(canon + ".backup.archived"); err != nil || len(got) == 0 {
		t.Fatalf("backup not archived beside it: %q %v", got, err)
	}
}

// The two races of the delete: a live log removed after it was listed (claude's sweep), and the
// backup loop publishing a different backup after the archive took its snapshot. Neither may
// lose the conversation: the snapshot is already in the archive.
func TestTrashRacesKeepTheBackupSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name string
		race func(t *testing.T, m session.Meta, canon, other string)
		want string // content restored at the canonical path; "" = the delete must be refused
	}{
		{"racesweep", func(t *testing.T, m session.Meta, canon, other string) {
			if err := os.Remove(canon); err != nil {
				t.Fatal(err)
			}
		}, "BACKUP"},
		{"racebackup", func(t *testing.T, m session.Meta, canon, other string) {
			// Canonical turns partial; a newer complete log appears elsewhere and the real backup
			// loop publishes it, replacing the project directory the snapshot came from.
			if err := os.WriteFile(canon, []byte(`{"type":"user","m":"INCOMPLETE`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(other), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(other, []byte(`{"type":"user","m":"NEWER"}`+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			future := time.Now().Add(time.Hour)
			if err := os.Chtimes(other, future, future); err != nil {
				t.Fatal(err)
			}
			if err := claude.BackupTranscript(m); err != nil {
				t.Fatal(err)
			}
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			m := session.Meta{Name: tc.name, Dir: dir, Kind: session.KindClaude, CreatedAt: time.Now().Format(time.RFC3339)}
			session.WriteMeta(m)
			id := session.UUID(dir, m.Name)
			canon := filepath.Join(home, "claude", "projects", claude.ProjectKey(dir), id+".jsonl")
			other := filepath.Join(home, "claude", "projects", "elsewhere", id+".jsonl")
			if err := os.MkdirAll(filepath.Dir(canon), 0o700); err != nil {
				t.Fatal(err)
			}
			backed := `{"type":"user","m":"BACKUP"}` + "\n"
			if err := os.WriteFile(canon, []byte(backed), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := claude.BackupTranscript(m); err != nil {
				t.Fatal(err)
			}
			archiveAfterMatch = func(cur session.Meta) { tc.race(t, cur, canon, other) }
			defer func() { archiveAfterMatch = nil }()

			var out struct{ Archive string }
			if tc.want == "" {
				// A log that appeared meanwhile is the trash's own "resumed" guard: nothing is
				// deleted, and the backup (now the newer conversation) is still there.
				do(t, srv, "DELETE", "/sessions/"+tc.name, nil, http.StatusConflict, nil)
				if _, ok := session.ReadMeta(tc.name); !ok {
					t.Fatal("the refused delete removed the meta")
				}
				if left, _ := filepath.Glob(filepath.Join(paths.AgentDataDir(), "claude-transcripts", id+"*")); len(left) == 0 {
					t.Fatal("the refused delete removed the backup")
				}
				return
			}
			do(t, srv, "DELETE", "/sessions/"+tc.name, nil, http.StatusOK, &out)
			_ = os.RemoveAll(filepath.Join(home, "claude", "projects"))
			do(t, srv, "POST", "/cleanup/archives/"+out.Archive+"/restore", nil, http.StatusOK, nil)
			got, err := os.ReadFile(canon)
			if err != nil || !strings.Contains(string(got), tc.want) {
				t.Fatalf("restored canonical = %q, %v; want the backup snapshot", got, err)
			}
		})
	}
}

// The live log's age and its bytes must come from one state of one file: swapped for an older
// complete log after its metadata was taken, it must not win the canonical path over the newer
// backup (ported from the round-5 review reproduction).
func TestArchiveLiveSwappedAfterStatDoesNotOutrankBackup(t *testing.T) {
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
	m := session.Meta{Name: "readage", Dir: dir, Kind: session.KindClaude, CreatedAt: time.Now().Format(time.RFC3339)}
	session.WriteMeta(m)
	jsonl := filepath.Join(home, "claude", "projects", claude.ProjectKey(dir), session.UUID(dir, m.Name)+".jsonl")
	if err := os.MkdirAll(filepath.Dir(jsonl), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"user","message":"KNOWN NEWER BACKUP"}` + "\n"
	if err := os.WriteFile(jsonl, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := claude.BackupTranscript(m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonl, []byte(`{"type":"user","message":"NEWER LIVE THAT WAS STATTED"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(jsonl, future, future); err != nil {
		t.Fatal(err)
	}
	archiveLiveReadGate = func(path string) {
		if path != jsonl {
			return
		}
		_ = os.WriteFile(jsonl, []byte(`{"type":"user","message":"OLD"}`+"\n"), 0o600)
		past := time.Now().Add(-24 * time.Hour)
		_ = os.Chtimes(jsonl, past, past)
	}
	defer func() { archiveLiveReadGate = nil }()
	var out struct{ Archive string }
	do(t, srv, "DELETE", "/sessions/"+m.Name, nil, http.StatusOK, &out)
	archiveLiveReadGate = nil
	do(t, srv, "POST", "/cleanup/archives/"+out.Archive+"/restore", nil, http.StatusOK, nil)
	got, err := os.ReadFile(jsonl)
	if err != nil || string(got) != body {
		aside, _ := os.ReadFile(jsonl + ".backup.archived")
		t.Fatalf("restored canonical = %q, %v; the newer backup is hidden beside it: %q", got, err, aside)
	}
}

// holdDeleteAt makes every delete that reaches step block there until release is closed, and
// reports each arrival on arrived. It is how a test sees a delete in its slow work, outside the gate.
func holdDeleteAt(t *testing.T, step string, release <-chan struct{}) (arrived chan string) {
	t.Helper()
	arrived = make(chan string, 8)
	restore := gitx.SetDeleteStepHook(func(s, dir string) {
		if s == step {
			arrived <- filepath.Base(dir)
			<-release
		}
	})
	t.Cleanup(restore)
	return arrived
}

// releaseOnce closes ch unless it is closed already, so a failing test can free the held deletes.
func releaseOnce(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func waitArrivals(t *testing.T, arrived <-chan string, n int, release chan struct{}) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-arrived:
		case <-time.After(10 * time.Second):
			releaseOnce(release) // let the held deletes finish so the failure is reported, not a hang
			t.Fatalf("only %d of %d deletes reached the held step: they are serialised", i, n)
		}
	}
}

// codeWithin is a request that can neither hang the test nor call t.Fatalf from a goroutine: it
// answers the status and body, or -1 when the request failed or took longer than 15 s.
func codeWithin(srv *httptest.Server, method, path string, body any) (int, string) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(b))
	if err != nil {
		return -1, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return -1, err.Error()
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw)
}

func twoWorktrees(t *testing.T) (home, parent, wt1, wt2 string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	parent = filepath.Join(home, "repos", "app")
	gitInit(t, parent)
	wt1 = filepath.Join(home, "repos", "app@one")
	wt2 = filepath.Join(home, "repos", "app@two")
	gitAt(t, parent, "worktree", "add", "-q", "-b", "one", wt1, "main")
	gitAt(t, parent, "worktree", "add", "-q", "-b", "two", wt2, "main")
	return home, parent, wt1, wt2
}

func lockAndRecreateMux() *http.ServeMux {
	mux := trashMux()
	mux.HandleFunc("POST /repos/{name}/lock", sessionx.HandleRepoLock)
	mux.HandleFunc("POST /sessions/{name}/lock", sessionx.HandleSessionLock)
	mux.HandleFunc("POST /repos/{name}/recreate", handleRecreateWorktree)
	mux.HandleFunc("GET /cleanup/archives", handleListCleanupArchives)
	return mux
}

// #4833: deletes of different working copies are not serialised by the deletion gate. Both must
// be inside `git worktree remove`, holding the base clone's lock together, at the same moment;
// with the old whole-delete gate, or a remove that takes the parent lock exclusively, the second
// never gets there and the test times out.
func TestDeletesOfDifferentWorktreesOverlap(t *testing.T) {
	_, parent, wt1, wt2 := twoWorktrees(t)
	srv := httptest.NewServer(trashMux())
	defer srv.Close()
	release := make(chan struct{})
	arrived := holdDeleteAt(t, "git-remove", release)
	defer releaseOnce(release)

	codes := make(chan int, 2)
	for _, wt := range []string{wt1, wt2} {
		go func() { c, _ := codeWithin(srv, "DELETE", "/repos/"+filepath.Base(wt)+"?force=1", nil); codes <- c }()
	}
	waitArrivals(t, arrived, 2, release)
	releaseOnce(release)
	for i := 0; i < 2; i++ {
		if code := <-codes; code != http.StatusOK {
			t.Fatalf("concurrent delete = %d, want 200", code)
		}
	}
	for _, wt := range []string{wt1, wt2} {
		if session.DirExists(wt) {
			t.Fatalf("%s was not deleted", wt)
		}
		if gitx.UnderDeletion(wt) {
			t.Fatalf("the mark of %s outlived its delete", wt)
		}
	}
	if n := gitx.LinkedWorktreeCount(parent); n != 0 {
		t.Fatalf("%d worktrees left registered after both deletes", n)
	}
	end, ok, why := gitx.BeginDelete(parent, "")
	if !ok {
		t.Fatalf("the base clone is still refused after its worktrees' deletes: %s", why)
	}
	end()
}

// #4833 / ADR 0101: a lock request that arrives while a delete is past its decision is refused
// (409), not answered "locked" and then deleted; the copy is deleted and no lock is left behind.
// The refusal holds through a symlink to the copy and for a session in a sub-folder reached
// through one, which the guards (real paths) see as the same place.
func TestLockDuringADeleteIsRefusedAndTheCopyIsDeleted(t *testing.T) {
	home, _, wt1, _ := twoWorktrees(t)
	srv := httptest.NewServer(lockAndRecreateMux())
	defer srv.Close()
	// A link inside the copy that leaves it: the session below lives behind it by path, which the
	// delete's guards match as a string prefix of the copy.
	linkTarget := filepath.Join(home, "linktarget", "sub")
	if err := os.MkdirAll(linkTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(linkTarget), filepath.Join(wt1, "out")); err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "inwt003", Dir: filepath.Join(wt1, "out", "sub"), Kind: session.KindCodex})
	alias := filepath.Join(home, "repos", "alias-one")
	if err := os.Symlink(wt1, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wt1, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(home, "elsewhere")
	if err := os.Symlink(wt1, outside); err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "inwt001", Dir: wt1, Kind: session.KindCodex})
	session.WriteMeta(session.Meta{Name: "inwt002", Dir: filepath.Join(outside, "sub"), Kind: session.KindCodex})
	release := make(chan struct{})
	arrived := holdDeleteAt(t, "git-remove", release)
	defer releaseOnce(release)

	done := make(chan int, 1)
	go func() { c, _ := codeWithin(srv, "DELETE", "/repos/app@one?force=1", nil); done <- c }()
	waitArrivals(t, arrived, 1, release)

	want409 := func(method, path string, body any) {
		t.Helper()
		if code, raw := codeWithin(srv, method, path, body); code != http.StatusConflict || !strings.Contains(raw, "delete_in_progress") {
			t.Fatalf("%s %s during a delete = %d %s, want 409 delete_in_progress", method, path, code, raw)
		}
	}
	// A session that came back after the delete decided (restored from the trash on its own, so
	// its name is not in the mark) and lives behind the link: matched by its stored path.
	session.WriteMeta(session.Meta{Name: "late001", Dir: filepath.Join(wt1, "out", "sub"), Kind: session.KindCodex})
	lock := map[string]any{"locked": true}
	for _, path := range []string{"/repos/app@one/lock", "/repos/alias-one/lock", "/sessions/inwt001/lock", "/sessions/inwt002/lock", "/sessions/inwt003/lock", "/sessions/late001/lock"} {
		want409("POST", path, lock)
	}
	// The same copy, also by another name, cannot be deleted twice at once; its base clone cannot
	// go under it; and a worktree of the same base clone cannot be recreated meanwhile.
	want409("DELETE", "/repos/app@one?force=1", nil)
	want409("DELETE", "/repos/alias-one?force=1", nil)
	want409("DELETE", "/repos/app?force=1", nil)
	releaseOnce(release)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("delete = %d, want 200", code)
	}
	if session.DirExists(wt1) || sessionx.RepoLocked(wt1) {
		t.Fatal("the worktree is still there, or a lock was recorded for it")
	}
	for _, n := range []string{"inwt001", "inwt002", "inwt003", "late001"} {
		if m, ok := session.ReadMeta(n); ok && m.Locked {
			t.Fatalf("session %s was locked although its copy was deleted", n)
		}
	}
}

// A failed delete clears its mark too: the refusal leaves the copy lockable and deletable.
func TestRefusedDeleteClearsItsMark(t *testing.T) {
	_, _, wt1, _ := twoWorktrees(t)
	srv := httptest.NewServer(lockAndRecreateMux())
	defer srv.Close()
	if err := os.WriteFile(filepath.Join(wt1, "dirty"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := httpStatus(t, srv, "DELETE", "/repos/app@one", nil); code != http.StatusConflict {
		t.Fatalf("delete of a dirty worktree = %d, want 409", code)
	}
	if code := httpStatus(t, srv, "POST", "/repos/app@one/lock", map[string]any{"locked": true}); code != http.StatusOK {
		t.Fatalf("lock after a refused delete = %d, want 200 (the mark leaked)", code)
	}
}

// A delete that waited at the gate while another delete of the same copy finished answers 404
// (the folder is gone), as it did when the existence check ran inside the gate; it must not
// report a second success for a path that is no longer there.
func TestSecondDeleteOfTheSameCopyAfterTheFirstFinishedIs404(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	gitInit(t, filepath.Join(home, "repos", "solo"))
	srv := httptest.NewServer(trashMux())
	defer srv.Close()

	atGate, goOn := make(chan struct{}), make(chan struct{})
	var first sync.Once
	t.Cleanup(gitx.SetDeleteStepHook(func(step, dir string) {
		if step != "pre-gate" {
			return
		}
		blocked := false
		first.Do(func() { blocked = true })
		if blocked {
			close(atGate)
			<-goOn
		}
	}))
	b := make(chan int, 1)
	go func() { c, _ := codeWithin(srv, "DELETE", "/repos/solo", nil); b <- c }()
	select {
	case <-atGate:
	case <-time.After(10 * time.Second):
		close(goOn)
		t.Fatal("the first delete never reached the gate")
	}
	if code, raw := codeWithin(srv, "DELETE", "/repos/solo", nil); code != http.StatusOK {
		close(goOn)
		t.Fatalf("delete A = %d %s, want 200", code, raw)
	}
	close(goOn)
	if code := <-b; code != http.StatusNotFound {
		t.Fatalf("delete B of a copy A already deleted = %d, want 404", code)
	}
}

// Past the removal, when `git worktree remove` has taken the folder and an alias of it no longer
// resolves, a lock on a session that lived behind the alias is still refused: the delete settles
// sessions it recorded by name inside the gate, not by path afterwards.
func TestLockOnASessionBehindABrokenAliasIsStillRefused(t *testing.T) {
	home, _, wt1, _ := twoWorktrees(t)
	srv := httptest.NewServer(lockAndRecreateMux())
	defer srv.Close()
	alias := filepath.Join(home, "repos", "alias-one")
	if err := os.Symlink(wt1, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wt1, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "viaal01", Dir: filepath.Join(alias, "sub"), Kind: session.KindCodex})
	release := make(chan struct{})
	arrived := holdDeleteAt(t, "shelve", release)
	defer releaseOnce(release)

	done := make(chan int, 1)
	go func() { c, _ := codeWithin(srv, "DELETE", "/repos/alias-one?force=1", nil); done <- c }()
	waitArrivals(t, arrived, 1, release)
	if session.DirExists(wt1) {
		t.Fatal("the folder is still there at the shelving step")
	}
	if code, raw := codeWithin(srv, "POST", "/sessions/viaal01/lock", map[string]any{"locked": true}); code != http.StatusConflict || !strings.Contains(raw, "delete_in_progress") {
		t.Fatalf("lock after the folder went = %d %s, want 409 delete_in_progress", code, raw)
	}
	releaseOnce(release)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("delete = %d, want 200", code)
	}
	if m, ok := session.ReadMeta("viaal01"); !ok || !m.Archived || m.Locked {
		t.Fatalf("session after the delete = %+v ok=%v, want shelved and unlocked", m, ok)
	}
}

// The window between a delete's existence check and its mark: a delete of the same copy that
// finishes while the second one sits in the gate must make the second answer 404, not 200.
func TestSecondDeleteThatSeesTheFirstFinishInsideTheGateIs404(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	dir := filepath.Join(home, "repos", "solo")
	gitInit(t, dir)
	srv := httptest.NewServer(trashMux())
	defer srv.Close()

	releaseA, bAtMark, goB, aInRemove := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer releaseOnce(releaseA)
	var marks atomic.Int32
	t.Cleanup(gitx.SetDeleteStepHook(func(step, d string) {
		switch step {
		case "remove":
			close(aInRemove)
			<-releaseA
		case "pre-mark":
			if marks.Add(1) == 2 { // the second delete, holding the gate
				close(bAtMark)
				<-goB
			}
		}
	}))
	aCode := make(chan int, 1)
	go func() { c, _ := codeWithin(srv, "DELETE", "/repos/solo", nil); aCode <- c }()
	select { // A holds its mark once it is in its removal
	case <-aInRemove:
	case c := <-aCode:
		t.Fatalf("delete A ended with %d before it reached its removal", c)
	case <-time.After(10 * time.Second):
		releaseOnce(releaseA)
		t.Fatal("delete A never reached its removal")
	}
	bCode := make(chan int, 1)
	go func() { c, _ := codeWithin(srv, "DELETE", "/repos/solo", nil); bCode <- c }()
	select {
	case <-bAtMark:
	case <-time.After(10 * time.Second):
		releaseOnce(releaseA)
		close(goB)
		t.Fatal("the second delete never reached the gate")
	}
	releaseOnce(releaseA)
	if code := <-aCode; code != http.StatusOK {
		close(goB)
		t.Fatalf("delete A = %d, want 200", code)
	}
	for deadline := time.Now().Add(10 * time.Second); gitx.UnderDeletion(dir) && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond) // A's mark is cleared after its reply
	}
	close(goB)
	if code := <-bCode; code != http.StatusNotFound {
		t.Fatalf("delete B that saw A finish inside the gate = %d, want 404", code)
	}
}

// Recreating a worktree and restoring one from the trash both run git against the base clone's
// registry, so a delete of any worktree of that base clone in flight refuses them (409), and once
// it is over they go through. The trash entry is a real one, made by an ordinary delete.
func TestRecreateAndRestoreWaitForADeleteOnTheSameBaseClone(t *testing.T) {
	_, _, wt1, wt2 := twoWorktrees(t)
	srv := httptest.NewServer(lockAndRecreateMux())
	defer srv.Close()
	do(t, srv, "DELETE", "/repos/app@two", nil, http.StatusOK, nil)
	arcs := worktreeArchives(t, srv)
	if len(arcs) != 1 {
		t.Fatalf("%d worktree archives after the delete, want 1", len(arcs))
	}
	release := make(chan struct{})
	arrived := holdDeleteAt(t, "git-remove", release)
	defer releaseOnce(release)
	done := make(chan int, 1)
	go func() { c, _ := codeWithin(srv, "DELETE", "/repos/app@one?force=1", nil); done <- c }()
	waitArrivals(t, arrived, 1, release)

	for _, req := range []struct{ path string }{{"/repos/app@two/recreate"}, {"/cleanup/archives/" + arcs[0].ID + "/restore"}} {
		code, raw := codeWithin(srv, "POST", req.path, map[string]any{"source": "deleted", "branch": "two"})
		if code != http.StatusConflict || !strings.Contains(raw, "delete_in_progress") {
			releaseOnce(release)
			t.Fatalf("POST %s during a delete on the same base clone = %d %s, want 409 delete_in_progress", req.path, code, raw)
		}
	}
	if session.DirExists(wt2) {
		t.Fatal("a refused recreate or restore created the folder")
	}
	releaseOnce(release)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("delete = %d, want 200", code)
	}
	if code := httpStatus(t, srv, "POST", "/repos/app@two/recreate", map[string]any{"source": "deleted", "branch": "two"}); code != http.StatusCreated {
		t.Fatalf("recreate after the delete = %d, want 201", code)
	}
	do(t, srv, "POST", "/cleanup/archives/"+arcs[0].ID+"/restore", nil, http.StatusOK, nil)
	_ = wt1
}

// A base clone that sits inside the copy being deleted, reached through a symlink under another
// name, would lose its registry under a worktree recreated now: refused while the outer delete runs.
func TestRecreateUnderABaseCloneInsideADeleteTargetIsRefused(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	repos := filepath.Join(home, "repos")
	outer := filepath.Join(repos, "outer")
	gitInit(t, outer)
	inner := filepath.Join(outer, "inner")
	gitInit(t, inner)
	if err := os.Symlink(inner, filepath.Join(repos, "nested")); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(lockAndRecreateMux())
	defer srv.Close()
	release := make(chan struct{})
	arrived := holdDeleteAt(t, "remove", release)
	defer releaseOnce(release)
	done := make(chan int, 1)
	go func() { c, _ := codeWithin(srv, "DELETE", "/repos/outer?force=1", nil); done <- c }()
	waitArrivals(t, arrived, 1, release)
	code, raw := codeWithin(srv, "POST", "/repos/nested@topic/recreate", map[string]any{"source": "local", "branch": "main"})
	if code != http.StatusConflict || !strings.Contains(raw, "delete_in_progress") {
		releaseOnce(release)
		t.Fatalf("recreate under a base clone inside the delete's target = %d %s, want 409 delete_in_progress", code, raw)
	}
	releaseOnce(release)
	<-done
}

// A panic while the second delete answers its 404 (the response writer failing) must not leave the
// mark it had just taken: net/http recovers the panic and the same path is deletable again.
func TestMarkIsNotLeakedWhenTheAnswerPanics(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	dir := filepath.Join(home, "repos", "solo")
	gitInit(t, dir)
	t.Cleanup(gitx.SetDeleteStepHook(func(step, d string) {
		if step == "pre-gate" {
			_ = os.RemoveAll(d) // the copy goes between the first existence check and the mark
		}
	}))
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /repos/{name}", func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = recover() }()
		gitx.HandleDeleteRepo(panicWriter{w}, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	_, _ = codeWithin(srv, "DELETE", "/repos/solo", nil)
	if gitx.UnderDeletion(dir) {
		t.Fatal("the mark outlived a delete whose answer panicked")
	}
}

type panicWriter struct{ http.ResponseWriter }

func (panicWriter) WriteHeader(int)           { panic("writer failed") }
func (panicWriter) Write([]byte) (int, error) { panic("writer failed") }
