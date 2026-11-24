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

// The trash row is listed from its sidecar alone, so a delete whose sidecar cannot be written is
// refused with the space where it was, instead of moving a folder no row names.
func TestDeleteSpaceRefusesWhenTheEntryCannotBeListed(t *testing.T) {
	e := spaceDeleteSetup(t)
	sp := newSpace(t, "Hidden")
	writeAt(t, filepath.Join(sp.Dir, "f.md"), "x")
	now := nowUTC()
	id := newCleanupID(now, idSlug("space-"+sp.Folder))
	// A directory where the sidecar goes: its best-effort write fails, the tarball's does not.
	if err := os.MkdirAll(filepath.Join(cleanupStoreDir(), id+".json"), 0o700); err != nil {
		t.Fatal(err)
	}
	prev := nowUTC
	nowUTC = func() time.Time { return now }
	t.Cleanup(func() { nowUTC = prev })

	if got := errCodeOf(t, e.srv, "DELETE", "/spaces/"+sp.Folder, http.StatusInternalServerError); got != errCodeSpaceTrashFailed {
		t.Errorf("code = %q, want %q", got, errCodeSpaceTrashFailed)
	}
	if b, _ := os.ReadFile(filepath.Join(sp.Dir, "f.md")); string(b) != "x" {
		t.Fatal("the space was moved although no row could name it")
	}
	if _, err := os.Lstat(filepath.Join(cleanupStoreDir(), id+".tar.gz")); !os.IsNotExist(err) {
		t.Fatalf("the refused delete left its tarball: %v", err)
	}
}
