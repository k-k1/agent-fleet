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
	home := t.TempDir()
	space := filepath.Join(home, "spaces", "notes")
	other := filepath.Join(home, "other")
	mkdirs(t, filepath.Join(space, "sub", "deep"), filepath.Join(other, "nested"), filepath.Join(home, "spaces", "notesx"))
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
	}{
		{"the folder itself", session.Meta{Dir: space}, true},
		{"below it", session.Meta{Dir: filepath.Join(space, "sub")}, true},
		{"trailing slash", session.Meta{Dir: space + "/"}, true},
		{"a sibling sharing the prefix", session.Meta{Dir: filepath.Join(home, "spaces", "notesx")}, false},
		{"through a link to it", session.Meta{Dir: link}, true},
		{"through a link below it", session.Meta{Dir: filepath.Join(inner, "deep")}, true},
		{"home with the subdir in it", session.Meta{Dir: home, Subdir: "spaces/notes/sub"}, true},
		{"home with the subdir elsewhere", session.Meta{Dir: home, Subdir: "other"}, false},
		{"home alone", session.Meta{Dir: home}, false},
		// A link before `..`: the kernel enters other/, which a lexical clean would not say.
		{"a link then .. out", session.Meta{Dir: jump + "/../nested"}, false},
		// Spelled through the folder: once it is gone the path no longer resolves.
		{"spelled through it", session.Meta{Dir: space + "/../notesx"}, true},
		{"no dir", session.Meta{}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := SessionLivesIn(c.m, space); got != c.want {
				t.Fatalf("SessionLivesIn(%+v) = %v, want %v", c.m, got, c.want)
			}
			if got := LivesIn(space)(c.m); got != c.want {
				t.Fatalf("LivesIn = %v, want %v", got, c.want)
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

// The shells a working-copy delete settles are read once, before its slower steps; one deleted on
// its own in between is not a failure (nothing is left listed), while one that is still listed and
// could not be trashed stops the delete.
func TestTrashShellSessionsSkipsOneDeletedSince(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	old := deps.TrashSession
	t.Cleanup(func() { deps.TrashSession = old })
	deps.TrashSession = func(m session.Meta) error { return errors.New("no such session: " + m.Name) }

	gone := session.Meta{Name: "shgone1", Dir: home, Kind: session.KindShell}
	w := httptest.NewRecorder()
	if !trashShellSessionsUnder(w, []session.Meta{gone}) {
		t.Fatalf("a shell deleted since stopped the delete: %d %s", w.Code, w.Body)
	}

	kept := session.Meta{Name: "shkept1", Dir: home, Kind: session.KindShell}
	session.WriteMeta(kept)
	w = httptest.NewRecorder()
	if trashShellSessionsUnder(w, []session.Meta{kept}) || w.Code != http.StatusInternalServerError {
		t.Fatalf("a shell still listed that could not be trashed: ok, %d", w.Code)
	}
}
