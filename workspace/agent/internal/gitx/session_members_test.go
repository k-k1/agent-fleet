package gitx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// One rule decides which sessions live in a folder, for every step of a delete: by Meta.Dir and by
// the folder the stored Subdir names, each by its spelling and by where the kernel takes it.
func TestSessionLivesIn(t *testing.T) {
	yes := true
	home := t.TempDir()
	space := filepath.Join(home, "spaces", "notes")
	other := filepath.Join(home, "other")
	mkdirs(t, filepath.Join(space, "sub", "deep"), filepath.Join(other, "nested"), filepath.Join(other, "spaces", "notes"), filepath.Join(home, "spaces", "notesx"))
	link := filepath.Join(home, "alias")
	if err := os.Symlink(space, link); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(home, "inner")
	if err := os.Symlink(filepath.Join(space, "sub"), inner); err != nil {
		t.Fatal(err)
	}
	jump := filepath.Join(home, "jump")
	if err := os.Symlink(filepath.Join(other, "nested"), jump); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		m    session.Meta
		want bool
		// guard is SessionMayRunIn when it differs from want.
		guard *bool
	}{
		{"the folder itself", session.Meta{Dir: space}, true, nil},
		{"below it", session.Meta{Dir: filepath.Join(space, "sub")}, true, nil},
		{"trailing slash", session.Meta{Dir: space + "/"}, true, nil},
		{"a sibling sharing the prefix", session.Meta{Dir: filepath.Join(home, "spaces", "notesx")}, false, nil},
		{"through a link to it", session.Meta{Dir: link}, true, nil},
		{"through a link below it", session.Meta{Dir: filepath.Join(inner, "deep")}, true, nil},
		{"home with the subdir in it", session.Meta{Dir: home, Subdir: "spaces/notes/sub"}, true, nil},
		{"home with the subdir elsewhere", session.Meta{Dir: home, Subdir: "other"}, false, nil},
		{"home alone", session.Meta{Dir: home}, false, nil},
		// A link before `..`: the kernel enters other/, which a lexical clean would not say.
		{"a link then .. out", session.Meta{Dir: jump + "/../nested"}, false, nil},
		// The lexical clean names the folder, the kernel goes elsewhere: a session started before
		// the link was re-pointed may still run in the folder, so the guards count it, a settle
		// does not.
		{"a link then .. back by spelling", session.Meta{Dir: jump + "/../spaces/notes"}, false, &yes},
		// Spelled through the folder: once it is gone the path no longer resolves.
		{"spelled through it", session.Meta{Dir: space + "/../notesx"}, true, nil},
		{"no dir", session.Meta{}, false, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := SessionLivesIn(c.m, space); got != c.want {
				t.Fatalf("SessionLivesIn(%+v) = %v, want %v", c.m, got, c.want)
			}
			guard := c.want
			if c.guard != nil {
				guard = *c.guard
			}
			if got := SessionMayRunIn(c.m, space); got != guard {
				t.Fatalf("SessionMayRunIn = %v, want %v", got, guard)
			}
			if got := MayRunIn(space)(c.m); got != guard {
				t.Fatalf("MayRunIn = %v, want %v", got, guard)
			}
		})
	}
	// The folder named through the link finds the same sessions.
	if !SessionLivesIn(session.Meta{Dir: filepath.Join(space, "sub")}, link) {
		t.Fatal("a delete named through a link misses a session spelled by the real path")
	}
	if SessionLivesIn(session.Meta{Dir: space}, "") {
		t.Fatal("an empty folder holds sessions")
	}
}

func fakeTmuxIn(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AF_TMUX_SOCKET", "")
}

// ReadDirSessions answers a short list never: an unreadable meta, or a tmux that cannot say what
// runs, is an error, and what it reads otherwise is split the way the guards and the settle steps
// ask for it.
func TestReadDirSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	dir := filepath.Join(home, "app")
	mkdirs(t, dir)
	session.WriteMeta(session.Meta{Name: "run0001", Dir: dir, Kind: session.KindShell})
	session.WriteMeta(session.Meta{Name: "stop001", Dir: home, Subdir: "app", Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "lock001", Dir: dir, Kind: session.KindClaude, Locked: true})
	session.WriteMeta(session.Meta{Name: "arch001", Dir: dir, Kind: session.KindClaude, Archived: true})
	session.WriteMeta(session.Meta{Name: "else001", Dir: home, Kind: session.KindClaude})

	fakeTmuxIn(t, "echo "+session.TmuxPrefix+"run0001\n")
	s, err := ReadDirSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := s.Names()
	sort.Strings(names)
	if want := []string{"arch001", "lock001", "run0001", "stop001"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("Names = %v, want %v", names, want)
	}
	if got := s.Running(); len(got) != 1 {
		t.Fatalf("Running = %v, want the one tmux lists", got)
	}
	if got := s.Locked(); len(got) != 1 {
		t.Fatalf("Locked = %v, want one", got)
	}
	if got := s.ToSettle(); len(got) != 1 || got[0].Name != "stop001" {
		t.Fatalf("ToSettle = %+v, want stop001 alone", got)
	}

	fakeTmuxIn(t, "echo 'error connecting to /tmp/tmux-1/default (Permission denied)' >&2\nexit 1\n")
	if _, err := ReadDirSessions(dir); err == nil {
		t.Fatal("a tmux that could not be asked read as nothing running")
	}

	fakeTmuxIn(t, "echo 'no server running on /tmp/tmux-1/default' >&2\nexit 1\n")
	if err := os.WriteFile(session.MetaPath("brok001"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDirSessions(dir); err == nil || !strings.Contains(err.Error(), "brok001") {
		t.Fatalf("an unreadable meta: err = %v, want one naming it", err)
	}
}

// The shells a delete settles are read once, before its slower steps, and each is read again
// before it goes to the trash: one deleted on its own in between is not a failure (nothing is left
// listed), one whose link was re-pointed out of the folder is left alone, and one still listed in
// the folder that could not be trashed stops the delete.
func TestTrashShellsBeforeRemovalReadsEachAgain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	dir := filepath.Join(home, "app")
	elsewhere := filepath.Join(home, "elsewhere")
	mkdirs(t, dir, elsewhere)
	var trashed []string
	old := deps.TrashSession
	t.Cleanup(func() { deps.TrashSession = old })
	deps.TrashSession = func(m session.Meta) error {
		trashed = append(trashed, m.Name)
		return errors.New("disk full")
	}

	gone := session.Meta{Name: "shgone1", Dir: dir, Kind: session.KindShell}
	moved := session.Meta{Name: "shmove1", Dir: dir, Kind: session.KindShell}
	session.WriteMeta(session.Meta{Name: "shmove1", Dir: elsewhere, Kind: session.KindShell})
	w := httptest.NewRecorder()
	if !trashShellSessionsUnder(w, dir, []session.Meta{gone, moved}) {
		t.Fatalf("a shell deleted or moved since stopped the delete: %d %s", w.Code, w.Body)
	}
	if len(trashed) != 0 {
		t.Fatalf("trashed %v, want none", trashed)
	}

	kept := session.Meta{Name: "shkept1", Dir: dir, Kind: session.KindShell}
	session.WriteMeta(kept)
	w = httptest.NewRecorder()
	if trashShellSessionsUnder(w, dir, []session.Meta{kept}) || w.Code != http.StatusInternalServerError {
		t.Fatalf("a shell still in the folder that could not be trashed: ok, %d", w.Code)
	}
}
