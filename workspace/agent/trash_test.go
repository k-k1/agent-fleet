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
