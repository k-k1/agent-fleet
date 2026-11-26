package main

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// tmuxSays puts a "tmux" first on PATH that runs script, so a test decides what the delete's
// read of the running sessions sees.
func tmuxSays(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AF_TMUX_SOCKET", "")
}

const (
	tmuxNoServer = "echo 'no server running on /tmp/tmux-1/default' >&2\nexit 1\n"
	tmuxDenied   = "echo 'error connecting to /tmp/tmux-1/default (Permission denied)' >&2\nexit 1\n"
)

// A space's delete that cannot tell which sessions live in it (a meta it cannot read, a tmux it
// cannot ask) answers 503 and changes nothing, rather than read the failure as "nothing there".
func TestDeleteSpaceRefusesWhatItCannotObserve(t *testing.T) {
	e := spaceDeleteSetup(t)
	sp := newSpace(t, "Watched")
	writeAt(t, filepath.Join(sp.Dir, "f.md"), "x")
	session.WriteMeta(session.Meta{Name: "aiobs01", Dir: sp.Dir, Kind: session.KindClaude})
	still := func(what string) {
		t.Helper()
		if b, _ := os.ReadFile(filepath.Join(sp.Dir, "f.md")); string(b) != "x" {
			t.Fatalf("%s: the space was changed", what)
		}
		if n := len(spaceArchives(t, e.srv)); n != 0 {
			t.Fatalf("%s: left %d trash entries", what, n)
		}
		if m, _ := session.ReadMeta("aiobs01"); m.Archived {
			t.Fatalf("%s: a session was shelved", what)
		}
	}

	tmuxSays(t, tmuxDenied)
	if got := errCodeOf(t, e.srv, "DELETE", "/spaces/"+sp.Folder, http.StatusServiceUnavailable); got != errCodeSpaceSessionsUnobservable {
		t.Errorf("tmux unreachable = %q", got)
	}
	still("tmux unreachable")

	tmuxSays(t, tmuxNoServer)
	if err := os.WriteFile(session.MetaPath("brokn01"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := errCodeOf(t, e.srv, "DELETE", "/spaces/"+sp.Folder, http.StatusServiceUnavailable); got != errCodeSpaceSessionsUnobservable {
		t.Errorf("unreadable meta = %q", got)
	}
	still("unreadable meta")

	// Once the record is readable again the delete goes through.
	if err := os.Remove(session.MetaPath("brokn01")); err != nil {
		t.Fatal(err)
	}
	do(t, e.srv, "DELETE", "/spaces/"+sp.Folder, nil, http.StatusOK, nil)
}

// A session lives in a space whichever way its path is spelt: started at home with its subdir in
// the space, or through a link to it. The guards refuse while it runs, and the delete settles it
// and records it in the entry when it is stopped — the same set at every step.
func TestDeleteSpaceSettlesEverySpellingOfItsSessions(t *testing.T) {
	e := spaceDeleteSetup(t)
	sp := newSpace(t, "Spelt")
	writeAt(t, filepath.Join(sp.Dir, "sub", "f.md"), "x")
	link := filepath.Join(e.home, "alias")
	if err := os.Symlink(sp.Dir, link); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Format(time.RFC3339)
	session.WriteMeta(session.Meta{Name: "aihome1", Dir: e.home, Subdir: "spaces/" + sp.Folder + "/sub", Kind: session.KindClaude, CreatedAt: now})
	session.WriteMeta(session.Meta{Name: "ailink1", Dir: link, Kind: session.KindClaude, CreatedAt: now})
	session.WriteMeta(session.Meta{Name: "shlink1", Dir: filepath.Join(link, "sub"), Kind: session.KindShell, CreatedAt: now})
	session.WriteMeta(session.Meta{Name: "aihome2", Dir: e.home, Kind: session.KindClaude, CreatedAt: now})

	// Running: each spelling blocks the delete on its own.
	for _, name := range []string{"aihome1", "ailink1"} {
		tmuxSays(t, "echo "+session.TmuxPrefix+name+"\n")
		if got := errCodeOf(t, e.srv, "DELETE", "/spaces/"+sp.Folder, http.StatusConflict); got != errCodeSpaceSessionsRunning {
			t.Fatalf("%s running = %q", name, got)
		}
	}
	// Locked: likewise.
	session.WriteMeta(session.Meta{Name: "aihome1", Dir: e.home, Subdir: "spaces/" + sp.Folder + "/sub", Kind: session.KindClaude, CreatedAt: now, Locked: true})
	tmuxSays(t, tmuxNoServer)
	if got := errCodeOf(t, e.srv, "DELETE", "/spaces/"+sp.Folder, http.StatusForbidden); got != errCodeSpaceLockedSessions {
		t.Fatalf("locked subdir session = %q", got)
	}
	session.WriteMeta(session.Meta{Name: "aihome1", Dir: e.home, Subdir: "spaces/" + sp.Folder + "/sub", Kind: session.KindClaude, CreatedAt: now})

	var out struct{ Archive string }
	do(t, e.srv, "DELETE", "/spaces/"+sp.Folder, nil, http.StatusOK, &out)
	arcs := spaceArchives(t, e.srv)
	if len(arcs) != 1 || arcs[0].Space == nil || strings.Join(arcs[0].Space.Shelved, ",") != "aihome1,ailink1" {
		t.Fatalf("trash entries = %+v", arcs)
	}
	for _, n := range []string{"aihome1", "ailink1"} {
		if m, ok := session.ReadMeta(n); !ok || !m.Archived {
			t.Fatalf("%s was not shelved: %+v %v", n, m, ok)
		}
	}
	if _, ok := session.ReadMeta("shlink1"); ok || !trashHolds(t, "shlink1") {
		t.Fatalf("the shell reached through the link: listed=%v in trash=%v, want it in the trash", ok, trashHolds(t, "shlink1"))
	}
	if m, ok := session.ReadMeta("aihome2"); !ok || m.Archived {
		t.Fatalf("a session at home outside the space was settled: %+v %v", m, ok)
	}
}

// A working copy's delete reads its sessions the same way: an unreadable meta or a tmux it cannot
// ask answers 503 and removes nothing.
func TestDeleteRepoRefusesWhatItCannotObserve(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	e := spaceDeleteSetup(t)
	dir := filepath.Join(e.home, "repos", "app")
	gitInit(t, dir)
	session.WriteMeta(session.Meta{Name: "shrepo1", Dir: dir, Kind: session.KindShell})

	tmuxSays(t, tmuxDenied)
	if got := errCodeOf(t, e.srv, "DELETE", "/repos/app", http.StatusServiceUnavailable); got != errCodeSessionsUnobservable {
		t.Errorf("tmux unreachable = %q", got)
	}
	tmuxSays(t, tmuxNoServer)
	if err := os.WriteFile(session.MetaPath("brokn02"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := errCodeOf(t, e.srv, "DELETE", "/repos/app", http.StatusServiceUnavailable); got != errCodeSessionsUnobservable {
		t.Errorf("unreadable meta = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("the working copy was removed: %v", err)
	}
	if _, ok := session.ReadMeta("shrepo1"); !ok {
		t.Fatal("the shell was trashed by a refused delete")
	}
	if err := os.Remove(session.MetaPath("brokn02")); err != nil {
		t.Fatal(err)
	}
	do(t, e.srv, "DELETE", "/repos/app", nil, http.StatusOK, nil)
	if _, ok := session.ReadMeta("shrepo1"); ok || !trashHolds(t, "shrepo1") {
		t.Fatal("the shell did not go to the trash with the working copy")
	}
}

// After the move the sessions decided before it are settled by name, whatever their path says
// now (a link into the folder may no longer resolve to it), and with their meta as it is now: one
// that is locked, running or gone since is left alone.
func TestShelveAfterTheMoveTakesTheDecidedSessionsByName(t *testing.T) {
	e := spaceDeleteSetup(t)
	gone := filepath.Join(e.home, "gone")
	elsewhere := filepath.Join(e.home, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, m := range []session.Meta{
		{Name: "aidec01", Dir: elsewhere, Kind: session.KindClaude},
		{Name: "aidec02", Dir: elsewhere, Kind: session.KindClaude, Locked: true},
		{Name: "aidec03", Dir: elsewhere, Kind: session.KindClaude},
		{Name: "ainot01", Dir: elsewhere, Kind: session.KindClaude},
	} {
		session.WriteMeta(m)
	}
	tmuxSays(t, "echo "+session.TmuxPrefix+"aidec03\n")
	gitx.ShelveSessionsUnder(gone, []session.Meta{{Name: "aidec01"}, {Name: "aidec02"}, {Name: "aidec03"}, {Name: "nometa1"}})
	if m, _ := session.ReadMeta("aidec01"); !m.Archived {
		t.Fatal("a decided session was not shelved")
	}
	for _, n := range []string{"aidec02", "aidec03", "ainot01"} {
		if m, _ := session.ReadMeta(n); m.Archived {
			t.Fatalf("%s was shelved", n)
		}
	}
}
