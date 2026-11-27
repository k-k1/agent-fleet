package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/spacex"
)

type spaceEnv struct {
	srv  *httptest.Server
	home string
}

func spaceDeleteSetup(t *testing.T) spaceEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	mux := trashMux()
	mux.HandleFunc("GET /spaces", spacex.HandleList)
	mux.HandleFunc("DELETE /spaces/{folder}", handleDeleteSpace)
	mux.HandleFunc("GET /cleanup/archives", handleListCleanupArchives)
	mux.HandleFunc("DELETE /cleanup/archives/{id}", handlePurgeCleanupArchive)
	mux.HandleFunc("POST /sessions/{name}/archive", sessionx.HandleArchiveSession)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return spaceEnv{srv: srv, home: home}
}

func writeAt(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newSpace(t *testing.T, name string) spacex.Space {
	t.Helper()
	sp, err := spacex.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	return sp
}

func spaceArchives(t *testing.T, srv *httptest.Server) []cleanupManifest {
	t.Helper()
	var list struct{ Archives []cleanupManifest }
	do(t, srv, "GET", "/cleanup/archives", nil, http.StatusOK, &list)
	var out []cleanupManifest
	for _, a := range list.Archives {
		if a.Reason == "delete_space" {
			out = append(out, a)
		}
	}
	return out
}

func spaceListed(t *testing.T, srv *httptest.Server, folder string) bool {
	t.Helper()
	var list struct{ Spaces []spacex.Space }
	do(t, srv, "GET", "/spaces", nil, http.StatusOK, &list)
	for _, s := range list.Spaces {
		if s.Folder == folder {
			return true
		}
	}
	return false
}

func errCodeOf(t *testing.T, srv *httptest.Server, method, path string, want int) string {
	t.Helper()
	var body struct{ Error struct{ Code string } }
	do(t, srv, method, path, nil, want, &body)
	return body.Error.Code
}

// Deleting a space moves its folder to the trash with every file, shelves its AI sessions and
// trashes its shell, and leaves the other spaces and the sessions in them alone. Restoring the
// entry brings the folder, its marker and the shelved sessions back, and does so once however
// often it is pressed; purging it is what finally removes the files.
func TestDeletedSpaceRoundTripsThroughTheTrash(t *testing.T) {
	e := spaceDeleteSetup(t)
	sp := newSpace(t, "Research notes")
	other := newSpace(t, "Other")
	writeAt(t, filepath.Join(sp.Dir, "draft.md"), "my draft")
	writeAt(t, filepath.Join(sp.Dir, "attachments", "data.csv"), "a,b")
	writeAt(t, filepath.Join(other.Dir, "keep.md"), "kept")
	mk := func(name, dir, kind string) {
		session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: kind, CreatedAt: time.Now().Format(time.RFC3339)})
	}
	mk("aispc01", sp.Dir, session.KindClaude)
	mk("aispc02", filepath.Join(sp.Dir, "sub"), session.KindClaude)
	mk("shspc01", sp.Dir, session.KindShell)
	mk("aioth01", other.Dir, session.KindClaude)
	mk("airepo1", filepath.Join(e.home, "repos", "app"), session.KindClaude)

	var out struct{ Deleted, Archive string }
	do(t, e.srv, "DELETE", "/spaces/"+sp.Folder, nil, http.StatusOK, &out)
	if out.Deleted != sp.Folder || out.Archive == "" {
		t.Fatalf("answer = %+v", out)
	}
	if _, err := os.Lstat(sp.Dir); !os.IsNotExist(err) {
		t.Fatalf("the space is still under ~/spaces: %v", err)
	}
	if spaceListed(t, e.srv, sp.Folder) {
		t.Fatal("the deleted space is still listed")
	}
	if b, err := os.ReadFile(filepath.Join(spaceTrashDir(out.Archive), "draft.md")); err != nil || string(b) != "my draft" {
		t.Fatalf("the trash does not hold the draft: %q %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(spaceTrashDir(out.Archive), "attachments", "data.csv")); err != nil || string(b) != "a,b" {
		t.Fatalf("the trash does not hold the attachment: %q %v", b, err)
	}
	arcs := spaceArchives(t, e.srv)
	if len(arcs) != 1 || arcs[0].Space == nil || arcs[0].Space.Folder != sp.Folder || arcs[0].Space.Name != "Research notes" ||
		strings.Join(arcs[0].Space.Shelved, ",") != "aispc01,aispc02" {
		t.Fatalf("trash entries = %+v", arcs)
	}
	if arcs[0].Bytes < int64(len("my draft")+len("a,b")) {
		t.Fatalf("the entry's size %d does not count the folder", arcs[0].Bytes)
	}
	for _, n := range []string{"aispc01", "aispc02"} {
		if m, ok := session.ReadMeta(n); !ok || !m.Archived {
			t.Fatalf("%s was not shelved: %+v %v", n, m, ok)
		}
	}
	if _, ok := session.ReadMeta("shspc01"); ok {
		t.Fatal("the space's shell was left listed")
	}
	if !trashHolds(t, "shspc01") {
		t.Fatal("the space's shell is not in the trash")
	}
	// Nothing else is touched: the other space, and the sessions outside this one.
	if b, _ := os.ReadFile(filepath.Join(other.Dir, "keep.md")); string(b) != "kept" {
		t.Fatal("another space lost a file")
	}
	for _, n := range []string{"aioth01", "airepo1"} {
		if m, ok := session.ReadMeta(n); !ok || m.Archived {
			t.Fatalf("%s was settled by another space's delete: %+v", n, m)
		}
	}

	for i := 0; i < 2; i++ { // twice: a second press only finds everything back
		var res struct{ Restored struct{ Space string } }
		do(t, e.srv, "POST", "/cleanup/archives/"+out.Archive+"/restore", nil, http.StatusOK, &res)
		if res.Restored.Space != sp.Folder {
			t.Fatalf("restored = %+v", res)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(sp.Dir, "draft.md")); string(b) != "my draft" {
		t.Fatalf("draft after restore = %q", b)
	}
	if got := spacex.FolderOf(sp.Dir); got != sp.Folder {
		t.Fatalf("the restored folder is no space: FolderOf = %q", got)
	}
	for _, n := range []string{"aispc01", "aispc02"} {
		if m, _ := session.ReadMeta(n); m.Archived {
			t.Fatalf("%s is still on the shelf", n)
		}
	}

	// Deleting again and purging removes the files for good.
	do(t, e.srv, "DELETE", "/spaces/"+sp.Folder, nil, http.StatusOK, &out)
	do(t, e.srv, "DELETE", "/cleanup/archives/"+out.Archive, nil, http.StatusOK, nil)
	if _, err := os.Lstat(spaceTrashDir(out.Archive)); !os.IsNotExist(err) {
		t.Fatalf("the purged entry's folder is still in the trash: %v", err)
	}
}

// A refusal changes nothing: a running session, a locked one, an unknown folder and a folder
// that only looks like one answer without moving the space.
func TestDeleteSpaceRefusals(t *testing.T) {
	e := spaceDeleteSetup(t)
	sp := newSpace(t, "Busy")
	writeAt(t, filepath.Join(sp.Dir, "f.md"), "x")
	still := func(what string) {
		t.Helper()
		if b, _ := os.ReadFile(filepath.Join(sp.Dir, "f.md")); string(b) != "x" {
			t.Fatalf("%s: the space was changed", what)
		}
		if n := len(spaceArchives(t, e.srv)); n != 0 {
			t.Fatalf("%s: left %d trash entries", what, n)
		}
	}

	if got := errCodeOf(t, e.srv, "DELETE", "/spaces/no-such-space", http.StatusNotFound); got != "space_not_found" {
		t.Errorf("unknown folder = %q", got)
	}
	// A folder by hand under ~/spaces has no marker, so it is no space and is never moved.
	writeAt(t, filepath.Join(e.home, "spaces", "mine", "f.md"), "hand")
	if got := errCodeOf(t, e.srv, "DELETE", "/spaces/mine", http.StatusNotFound); got != "space_not_found" {
		t.Errorf("unmarked folder = %q", got)
	}
	if _, err := os.Stat(filepath.Join(e.home, "spaces", "mine", "f.md")); err != nil {
		t.Errorf("an unmarked folder was moved: %v", err)
	}

	// Locked: neither stopped nor running gets past it.
	session.WriteMeta(session.Meta{Name: "lockd01", Dir: sp.Dir, Kind: session.KindClaude, Locked: true})
	if got := errCodeOf(t, e.srv, "DELETE", "/spaces/"+sp.Folder, http.StatusForbidden); got != errCodeSpaceLockedSessions {
		t.Errorf("locked session = %q", got)
	}
	still("locked")
	session.WriteMeta(session.Meta{Name: "lockd01", Dir: sp.Dir, Kind: session.KindClaude})

	// Running: a tmux that lists the session as live.
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in list-sessions) echo " + session.TmuxPrefix + "lockd01;; esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if got := errCodeOf(t, e.srv, "DELETE", "/spaces/"+sp.Folder, http.StatusConflict); got != errCodeSpaceSessionsRunning {
		t.Errorf("running session = %q", got)
	}
	still("running")
	if m, _ := session.ReadMeta("lockd01"); m.Archived {
		t.Error("a refused delete shelved a session")
	}
}

// Restoring refuses to put a space on top of something that is there, and says so with a code the
// Console words; the trash entry stays, so the restore can be pressed again once the way is clear.
func TestRestoreSpaceRefusesAnOccupiedFolder(t *testing.T) {
	e := spaceDeleteSetup(t)
	sp := newSpace(t, "Taken")
	writeAt(t, filepath.Join(sp.Dir, "f.md"), "x")
	var out struct{ Archive string }
	do(t, e.srv, "DELETE", "/spaces/"+sp.Folder, nil, http.StatusOK, &out)

	// Something other than a space sits at the folder's name (an empty directory would be
	// replaced by a rename, so it has to count too).
	if err := os.MkdirAll(sp.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := errCodeOf(t, e.srv, "POST", "/cleanup/archives/"+out.Archive+"/restore", http.StatusConflict); got != errCodeSpaceRestoreExists {
		t.Errorf("occupied = %q, want %q", got, errCodeSpaceRestoreExists)
	}
	if _, err := os.Stat(filepath.Join(spaceTrashDir(out.Archive), "f.md")); err != nil {
		t.Fatalf("a refused restore touched the trash: %v", err)
	}
	if err := os.Remove(sp.Dir); err != nil {
		t.Fatal(err)
	}
	do(t, e.srv, "POST", "/cleanup/archives/"+out.Archive+"/restore", nil, http.StatusOK, nil)
	if b, _ := os.ReadFile(filepath.Join(sp.Dir, "f.md")); string(b) != "x" {
		t.Fatalf("f.md after the retry = %q", b)
	}

	// Entry whose folder was removed from the trash by hand, and no space is back.
	do(t, e.srv, "DELETE", "/spaces/"+sp.Folder, nil, http.StatusOK, &out)
	if err := os.RemoveAll(spaceTrashDir(out.Archive)); err != nil {
		t.Fatal(err)
	}
	if got := errCodeOf(t, e.srv, "POST", "/cleanup/archives/"+out.Archive+"/restore", http.StatusConflict); got != errCodeSpaceRestoreMissing {
		t.Errorf("missing = %q, want %q", got, errCodeSpaceRestoreMissing)
	}
}

// A trash that cannot take what the delete needs stops it before the folder moves: the shell is
// the first thing trashed, and a delete that moved the folder first would leave that session
// listed with no folder.
func TestDeleteSpaceChangesNothingWhenTheTrashFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	e := spaceDeleteSetup(t)
	sp := newSpace(t, "Stuck")
	writeAt(t, filepath.Join(sp.Dir, "f.md"), "x")
	session.WriteMeta(session.Meta{Name: "shstck1", Dir: sp.Dir, Kind: session.KindShell})
	session.WriteMeta(session.Meta{Name: "aistck1", Dir: sp.Dir, Kind: session.KindClaude})
	store := cleanupStoreDir()
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(store, 0o700) })

	if got := errCodeOf(t, e.srv, "DELETE", "/spaces/"+sp.Folder, http.StatusInternalServerError); got != errCodeSpaceSessionsTrashFailed {
		t.Errorf("code = %q, want %q", got, errCodeSpaceSessionsTrashFailed)
	}
	if b, _ := os.ReadFile(filepath.Join(sp.Dir, "f.md")); string(b) != "x" {
		t.Fatal("the space was changed")
	}
	for _, n := range []string{"shstck1", "aistck1"} {
		if m, ok := session.ReadMeta(n); !ok || m.Archived {
			t.Fatalf("%s was settled although the delete was refused: %+v %v", n, m, ok)
		}
	}
}

// With no usable trash at all, nothing is moved either.
func TestDeleteSpaceWithoutATrashDirectoryChangesNothing(t *testing.T) {
	e := spaceDeleteSetup(t)
	sp := newSpace(t, "NoTrash")
	writeAt(t, filepath.Join(sp.Dir, "f.md"), "x")
	store := cleanupStoreDir()
	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store, nil, 0o600); err != nil { // a file where the trash directory should be
		t.Fatal(err)
	}
	if got := errCodeOf(t, e.srv, "DELETE", "/spaces/"+sp.Folder, http.StatusInternalServerError); got != errCodeSpaceTrashFailed {
		t.Errorf("code = %q, want %q", got, errCodeSpaceTrashFailed)
	}
	if b, _ := os.ReadFile(filepath.Join(sp.Dir, "f.md")); string(b) != "x" {
		t.Fatal("the space was changed")
	}
}

func TestDeleteSpaceRollsBackShellsOnPartialFailure(t *testing.T) {
	for _, failure := range []string{"second archive", "second stage", "manifest", "move"} {
		t.Run(failure, func(t *testing.T) {
			e := spaceDeleteSetup(t)
			tmuxSays(t, tmuxNoServer)
			sp := newSpace(t, "Rollback")
			writeAt(t, filepath.Join(sp.Dir, "f.md"), "contents")
			names := []string{"shroll1", "shroll2"}
			for _, name := range names {
				session.WriteMeta(session.Meta{Name: name, Kind: session.KindShell, Dir: sp.Dir, Title: name})
				writeAt(t, terminalHistoryPath(name), "terminal "+name)
			}
			oldClock, oldShell, oldSpace := nowUTC, trashAfterArchive, spaceAfterManifest
			oldStage := stageShellMetaRemoval
			t.Cleanup(func() { nowUTC, trashAfterArchive, spaceAfterManifest = oldClock, oldShell, oldSpace })
			t.Cleanup(func() { stageShellMetaRemoval = oldStage })
			nowUTC = func() time.Time { return time.Date(2026, 1, 1, 1, 1, 1, 0, time.UTC) }
			wantCode := errCodeSpaceTrashFailed
			switch failure {
			case "second archive":
				wantCode = errCodeSpaceSessionsTrashFailed
				trashAfterArchive = func(m session.Meta) {
					if m.Name == names[0] {
						conflict := filepath.Join(cleanupStoreDir(), newCleanupID(nowUTC(), names[1])+".json")
						_ = os.Mkdir(conflict, 0o700)
					}
				}
			case "second stage":
				wantCode = errCodeSpaceSessionsTrashFailed
				stageShellMetaRemoval = func(name string) (session.StagedMeta, error) {
					if name == names[1] {
						if _, st := session.ReadMetaState(names[0]); st != session.MetaAbsent {
							t.Error("the first meta was never staged")
						}
						return session.StagedMeta{}, os.ErrPermission
					}
					return oldStage(name)
				}
			case "manifest":
				spaceAfterManifest = func(id string) { _ = os.Remove(filepath.Join(cleanupStoreDir(), id+".json")) }
			case "move":
				spaceAfterManifest = func(id string) { _ = os.Mkdir(spaceTrashDir(id), 0o700) }
			}
			if code := errCodeOf(t, e.srv, "DELETE", "/spaces/"+sp.Folder, http.StatusInternalServerError); code != wantCode {
				t.Fatalf("delete code = %q, want %q", code, wantCode)
			}
			if got, _ := os.ReadFile(filepath.Join(sp.Dir, "f.md")); string(got) != "contents" {
				t.Fatalf("space contents = %q", got)
			}
			for _, name := range names {
				if m, ok := session.ReadMeta(name); !ok || m.Archived || m.Title != name {
					t.Fatalf("session %s after failure = %+v, present %v", name, m, ok)
				}
				if got, _ := os.ReadFile(terminalHistoryPath(name)); string(got) != "terminal "+name {
					t.Fatalf("session %s lost history: %q", name, got)
				}
				if trashHolds(t, name) {
					t.Fatalf("failed delete left a duplicate archive for %s", name)
				}
			}
			trashAfterArchive, spaceAfterManifest = nil, nil
			stageShellMetaRemoval = oldStage
			if failure == "second archive" {
				if err := os.Remove(filepath.Join(cleanupStoreDir(), newCleanupID(nowUTC(), names[1])+".json")); err != nil {
					t.Fatal(err)
				}
			}
			do(t, e.srv, "DELETE", "/spaces/"+sp.Folder, nil, http.StatusOK, nil)
			for _, name := range names {
				if _, ok := session.ReadMeta(name); ok || !trashHolds(t, name) {
					t.Fatalf("session %s was not trashed on retry", name)
				}
			}
		})
	}
}

func TestDeleteAndRestoreSpaceReportShelfWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	e := spaceDeleteSetup(t)
	tmuxSays(t, tmuxNoServer)
	sp := newSpace(t, "Shelf")
	name := "aishelf"
	session.WriteMeta(session.Meta{Name: name, Kind: session.KindClaude, Dir: sp.Dir})
	old := spaceAfterManifest
	t.Cleanup(func() { spaceAfterManifest = old; _ = os.Chmod(session.MetaDir(), 0o700) })
	var id string
	setMode := func(mode os.FileMode) {
		t.Helper()
		if err := os.Chmod(session.MetaDir(), mode); err != nil {
			t.Fatal(err)
		}
	}
	spaceAfterManifest = func(archive string) {
		id = archive
		if err := os.Chmod(session.MetaDir(), 0o500); err != nil {
			t.Error(err)
		}
	}
	if code := errCodeOf(t, e.srv, "DELETE", "/spaces/"+sp.Folder, http.StatusInternalServerError); code != errCodeSpaceSessionsSettleFailed {
		t.Fatalf("delete code = %q, want %q", code, errCodeSpaceSessionsSettleFailed)
	}
	if m, ok := session.ReadMeta(name); !ok || m.Archived {
		t.Fatalf("failed shelf no longer leaves a session to retry: %+v, present %v", m, ok)
	}
	setMode(0o700)
	do(t, e.srv, "POST", "/sessions/"+name+"/archive", nil, http.StatusOK, nil)
	setMode(0o500)
	path := "/cleanup/archives/" + id + "/restore"
	if code := errCodeOf(t, e.srv, "POST", path, http.StatusConflict); code != "restore_incomplete" {
		t.Fatalf("restore code = %q, want restore_incomplete", code)
	}
	if m, ok := session.ReadMeta(name); !ok || !m.Archived {
		t.Fatalf("failed restore no longer leaves a session to retry: %+v, present %v", m, ok)
	}
	setMode(0o700)
	do(t, e.srv, "POST", path, nil, http.StatusOK, nil)
	if m, ok := session.ReadMeta(name); !ok || m.Archived {
		t.Fatalf("restore retry did not unshelve the session: %+v, present %v", m, ok)
	}
}

// A purge pressed on the new trash row while the delete is still between publishing the entry and
// moving the folder waits for it, so the folder is never left in the trash with no entry to name
// it. Without the shared lock the purge removed the manifest first and the move then stranded the
// files.
func TestPurgeCannotOvertakeTheDeleteOfTheSameSpace(t *testing.T) {
	e := spaceDeleteSetup(t)
	sp := newSpace(t, "Racing")
	writeAt(t, filepath.Join(sp.Dir, "f.md"), "x")

	purged := make(chan int, 1)
	spaceAfterManifest = func(id string) {
		go func() { purged <- httpStatus(t, e.srv, "DELETE", "/cleanup/archives/"+id, nil) }()
		select {
		case code := <-purged:
			t.Errorf("the purge ran inside the delete (status %d)", code)
			purged <- code
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.Cleanup(func() { spaceAfterManifest = nil })

	var out struct{ Archive string }
	do(t, e.srv, "DELETE", "/spaces/"+sp.Folder, nil, http.StatusOK, &out)
	// The purge then runs on the finished entry and takes the folder with it.
	if code := <-purged; code != http.StatusOK {
		t.Fatalf("purge after the delete = %d", code)
	}
	if _, err := os.Lstat(spaceTrashDir(out.Archive)); !os.IsNotExist(err) {
		t.Fatalf("the folder was stranded in the trash: %v", err)
	}
	if n := len(spaceArchives(t, e.srv)); n != 0 {
		t.Fatalf("%d entries left", n)
	}
}

// pinSpaceID makes the next delete's trash id the one returned, and gives it back.
func pinSpaceID(t *testing.T, id string) {
	t.Helper()
	prev := newSpaceArchiveID
	newSpaceArchiveID = func(time.Time, string) string { return id }
	t.Cleanup(func() { newSpaceArchiveID = prev })
}

// The trash row is listed from its sidecar alone, so a delete whose sidecar cannot be written whole
// is refused with the space where it was, instead of moving a folder no row names. Both ways it
// fails are covered: nothing written (a directory in the way) and a write cut short (a read-only
// file that stays behind with part of its content).
func TestDeleteSpaceRefusesWhenTheEntryCannotBeListed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, path string)
	}{
		{"directory in the way", func(t *testing.T, p string) {
			if err := os.MkdirAll(p, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"cut-short file", func(t *testing.T, p string) {
			if os.Geteuid() == 0 {
				t.Skip("a read-only file does not stop root")
			}
			if err := os.WriteFile(p, []byte(`{"id": "20261124-1`), 0o400); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := spaceDeleteSetup(t)
			sp := newSpace(t, "Hidden")
			writeAt(t, filepath.Join(sp.Dir, "f.md"), "x")
			id := "20261124-120000-space-hidden-aaaaaaaa"
			pinSpaceID(t, id)
			if err := os.MkdirAll(cleanupStoreDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			tc.plant(t, filepath.Join(cleanupStoreDir(), id+".json"))
			if got := errCodeOf(t, e.srv, "DELETE", "/spaces/"+sp.Folder, http.StatusInternalServerError); got != errCodeSpaceTrashFailed {
				t.Errorf("code = %q, want %q", got, errCodeSpaceTrashFailed)
			}
			if b, _ := os.ReadFile(filepath.Join(sp.Dir, "f.md")); string(b) != "x" {
				t.Fatal("the space was moved although no row could name it")
			}
			if _, err := os.Lstat(filepath.Join(cleanupStoreDir(), id+".tar.gz")); !os.IsNotExist(err) {
				t.Fatalf("the refused delete left its tarball: %v", err)
			}
			// What was there before is still there: the delete wrote and removed nothing of its own
			// under a taken id.
			if _, err := os.Lstat(filepath.Join(cleanupStoreDir(), id+".json")); err != nil {
				t.Fatalf("the refused delete removed the file that was in its way: %v", err)
			}
		})
	}
}

// The sidecar this delete wrote itself and cut short: the one case the free-id check cannot
// reach. The read-back refuses it, the space stays, and what the attempt wrote is removed.
func TestDeleteSpaceRefusesASidecarItWroteCutShort(t *testing.T) {
	e := spaceDeleteSetup(t)
	sp := newSpace(t, "Cut")
	writeAt(t, filepath.Join(sp.Dir, "f.md"), "x")
	var drawn string
	spaceAfterManifest = func(id string) {
		drawn = id
		if err := os.WriteFile(filepath.Join(cleanupStoreDir(), id+".json"), []byte(`{"id": "20261`), 0o600); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { spaceAfterManifest = nil })

	if got := errCodeOf(t, e.srv, "DELETE", "/spaces/"+sp.Folder, http.StatusInternalServerError); got != errCodeSpaceTrashFailed {
		t.Errorf("code = %q, want %q", got, errCodeSpaceTrashFailed)
	}
	if b, _ := os.ReadFile(filepath.Join(sp.Dir, "f.md")); string(b) != "x" {
		t.Fatal("the space was moved although no row could name it")
	}
	for _, ext := range []string{".json", ".tar.gz", ".space"} {
		if _, err := os.Lstat(filepath.Join(cleanupStoreDir(), drawn+ext)); !os.IsNotExist(err) {
			t.Errorf("%s was left behind: %v", ext, err)
		}
	}
}

// A folder already waiting in the trash under the id a new delete drew belongs to another entry
// (whose tarball may be gone). The delete is refused, and the clean-up of its own files leaves that
// folder alone: a purge-shaped clean-up used to remove it.
func TestDeleteSpaceNeverRemovesAnotherEntrysFolder(t *testing.T) {
	e := spaceDeleteSetup(t)
	sp := newSpace(t, "Collide")
	writeAt(t, filepath.Join(sp.Dir, "f.md"), "x")
	id := "20261124-120000-space-collide-bbbbbbbb"
	pinSpaceID(t, id)
	writeAt(t, filepath.Join(spaceTrashDir(id), "theirs.md"), "other entry's file")

	if got := errCodeOf(t, e.srv, "DELETE", "/spaces/"+sp.Folder, http.StatusInternalServerError); got != errCodeSpaceTrashFailed {
		t.Errorf("code = %q, want %q", got, errCodeSpaceTrashFailed)
	}
	if b, _ := os.ReadFile(filepath.Join(spaceTrashDir(id), "theirs.md")); string(b) != "other entry's file" {
		t.Fatal("the other entry's folder was touched")
	}
	if b, _ := os.ReadFile(filepath.Join(sp.Dir, "f.md")); string(b) != "x" {
		t.Fatal("the space was moved")
	}
}

// A sidecar that names another id than its file is not trusted: the list skips it, so a purge by
// age cannot be steered at another entry's folder.
func TestListIgnoresASidecarThatNamesAnotherEntry(t *testing.T) {
	e := spaceDeleteSetup(t)
	sp := newSpace(t, "Real")
	writeAt(t, filepath.Join(sp.Dir, "f.md"), "x")
	var out struct{ Archive string }
	do(t, e.srv, "DELETE", "/spaces/"+sp.Folder, nil, http.StatusOK, &out)
	// An old entry whose sidecar was corrupted to carry the new entry's id.
	old := "20200101-000000-space-old-cccccccc"
	b, _ := os.ReadFile(filepath.Join(cleanupStoreDir(), out.Archive+".json"))
	if err := os.WriteFile(filepath.Join(cleanupStoreDir(), old+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	var list struct{ Archives []cleanupManifest }
	do(t, e.srv, "GET", "/cleanup/archives", nil, http.StatusOK, &list)
	for _, a := range list.Archives {
		if a.ID == old {
			t.Fatalf("a sidecar of another id was listed: %+v", a)
		}
	}
	if len(list.Archives) != 1 || list.Archives[0].ID != out.Archive {
		t.Fatalf("list = %+v, want the one real entry", list.Archives)
	}
}

// A restore and a purge of the same entry never overlap: with the restore inside its lock, the
// purge waits and finds the folder already back; with the purge first, the restore finds no entry
// and moves nothing.
func TestRestoreAndPurgeOfOneSpaceEntryAreSerialised(t *testing.T) {
	e := spaceDeleteSetup(t)
	sp := newSpace(t, "Both")
	writeAt(t, filepath.Join(sp.Dir, "f.md"), "x")
	var out struct{ Archive string }
	do(t, e.srv, "DELETE", "/spaces/"+sp.Folder, nil, http.StatusOK, &out)

	purged := make(chan int, 1)
	spaceBeforeMoveIn = func() {
		go func() { purged <- httpStatus(t, e.srv, "DELETE", "/cleanup/archives/"+out.Archive, nil) }()
		select {
		case code := <-purged:
			t.Errorf("the purge ran inside the restore (status %d)", code)
			purged <- code
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.Cleanup(func() { spaceBeforeMoveIn = nil })
	do(t, e.srv, "POST", "/cleanup/archives/"+out.Archive+"/restore", nil, http.StatusOK, nil)
	<-purged
	if b, _ := os.ReadFile(filepath.Join(sp.Dir, "f.md")); string(b) != "x" {
		t.Fatalf("the purge took files the restore had put back: %q", b)
	}
	spaceBeforeMoveIn = nil

	// Purge first: the restore has nothing to move.
	do(t, e.srv, "DELETE", "/spaces/"+sp.Folder, nil, http.StatusOK, &out)
	do(t, e.srv, "DELETE", "/cleanup/archives/"+out.Archive, nil, http.StatusOK, nil)
	if code := httpStatus(t, e.srv, "POST", "/cleanup/archives/"+out.Archive+"/restore", nil); code < 400 {
		t.Fatalf("restore after the purge = %d, want a refusal", code)
	}
	if _, err := os.Lstat(sp.Dir); !os.IsNotExist(err) {
		t.Fatalf("a purged space came back: %v", err)
	}
}

// A sidecar already on disk under the drawn id is another entry's, writable or not: the delete
// refuses before writing and leaves both it and the space as they were.
func TestDeleteSpaceLeavesAnExistingSidecarAlone(t *testing.T) {
	e := spaceDeleteSetup(t)
	sp := newSpace(t, "Taken")
	writeAt(t, filepath.Join(sp.Dir, "f.md"), "x")
	id := "20261124-120000-space-taken-dddddddd"
	pinSpaceID(t, id)
	const theirs = `{"id":"20261124-120000-space-taken-dddddddd","reason":"delete_session"}`
	if err := os.MkdirAll(cleanupStoreDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cleanupStoreDir(), id+".json"), []byte(theirs), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := errCodeOf(t, e.srv, "DELETE", "/spaces/"+sp.Folder, http.StatusInternalServerError); got != errCodeSpaceTrashFailed {
		t.Errorf("code = %q, want %q", got, errCodeSpaceTrashFailed)
	}
	if b, _ := os.ReadFile(filepath.Join(cleanupStoreDir(), id+".json")); string(b) != theirs {
		t.Fatalf("the existing sidecar was changed: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(sp.Dir, "f.md")); string(b) != "x" {
		t.Fatal("the space was moved")
	}
}

// The space part of a restore checks the entry again under its own lock: a purge that wins after
// the generic restore's check is refused too.
func TestRestoreLosesToAPurgeThatWonAfterTheGenericCheck(t *testing.T) {
	e := spaceDeleteSetup(t)
	sp := newSpace(t, "Later")
	writeAt(t, filepath.Join(sp.Dir, "f.md"), "x")
	var out struct{ Archive string }
	do(t, e.srv, "DELETE", "/spaces/"+sp.Folder, nil, http.StatusOK, &out)
	spaceRestoreStart = func() {
		do(t, e.srv, "DELETE", "/cleanup/archives/"+out.Archive, nil, http.StatusOK, nil)
	}
	t.Cleanup(func() { spaceRestoreStart = nil })
	if code := httpStatus(t, e.srv, "POST", "/cleanup/archives/"+out.Archive+"/restore", nil); code < 400 {
		t.Fatalf("restore after a purge that won = %d, want a refusal", code)
	}
	if _, err := os.Lstat(sp.Dir); !os.IsNotExist(err) {
		t.Fatalf("a purged space came back: %v", err)
	}
}

// A purge that wins after a restore read its manifest and before it takes the lock again: the
// restore is refused and the folder stays purged.
func TestRestoreLosesToAPurgeThatWonAfterItReadTheEntry(t *testing.T) {
	e := spaceDeleteSetup(t)
	sp := newSpace(t, "Late")
	writeAt(t, filepath.Join(sp.Dir, "f.md"), "x")
	var out struct{ Archive string }
	do(t, e.srv, "DELETE", "/spaces/"+sp.Folder, nil, http.StatusOK, &out)
	restoreAfterStage = func() {
		do(t, e.srv, "DELETE", "/cleanup/archives/"+out.Archive, nil, http.StatusOK, nil)
	}
	t.Cleanup(func() { restoreAfterStage = nil })
	if code := httpStatus(t, e.srv, "POST", "/cleanup/archives/"+out.Archive+"/restore", nil); code < 400 {
		t.Fatalf("restore after a purge that won = %d, want a refusal", code)
	}
	if _, err := os.Lstat(sp.Dir); !os.IsNotExist(err) {
		t.Fatalf("a purged space came back: %v", err)
	}
}

// A purge of a trashed space whose folder holds a directory without owner write (Go's module
// cache is mode 0555): a plain RemoveAll failed with EACCES on every press and the entry stayed
// in the trash for good.
func TestPurgeRemovesATrashedSpaceHoldingAReadOnlyDirectory(t *testing.T) {
	e := spaceDeleteSetup(t)
	sp := newSpace(t, "ReadOnly")
	writeAt(t, filepath.Join(sp.Dir, "f.md"), "x")
	mod := filepath.Join(sp.Dir, "go", "pkg", "mod", "example.com", "m@v1")
	writeAt(t, filepath.Join(mod, "m.go"), "package m")
	for _, d := range []string{mod, filepath.Join(sp.Dir, "go", "pkg", "mod")} {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	var out struct{ Archive string }
	do(t, e.srv, "DELETE", "/spaces/"+sp.Folder, nil, http.StatusOK, &out)
	trashed := spaceTrashDir(out.Archive)
	t.Cleanup(func() { // a red run must not leave a tree the test directory's own cleanup cannot remove
		_ = filepath.WalkDir(trashed, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o755)
			}
			return nil
		})
	})
	probe := filepath.Join(trashed, "go", "pkg", "mod", "probe-write")
	if err := os.WriteFile(probe, nil, 0o600); err == nil {
		_ = os.Remove(probe)
		t.Skip("directory permissions are not enforced here (running as root)")
	}

	if code := httpStatus(t, e.srv, "DELETE", "/cleanup/archives/"+out.Archive, nil); code != http.StatusOK {
		t.Fatalf("purge = %d, want 200", code)
	}
	if _, err := os.Lstat(trashed); !os.IsNotExist(err) {
		t.Fatalf("the trashed folder is still there: %v", err)
	}
	if n := len(spaceArchives(t, e.srv)); n != 0 {
		t.Fatalf("%d entries left in the trash", n)
	}
}
