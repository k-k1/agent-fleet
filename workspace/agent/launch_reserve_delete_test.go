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

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// A session being started in a space has no running process or meta the delete's guards can
// see yet; its reservation (#5122) is what refuses the delete. The launch here runs at home with
// its cwd inside the space, the case a match on Meta.Dir alone misses. Once the launch is over the
// delete goes through.
func TestDeleteSpaceRefusesWhileASessionIsStarting(t *testing.T) {
	e := spaceDeleteSetup(t)
	sp := newSpace(t, "Starting")
	writeAt(t, filepath.Join(sp.Dir, "f.md"), "x")

	release, ok := gitx.ReserveLaunch(e.home, filepath.Join(sp.Dir, "sub"))
	if !ok {
		t.Fatal("reservation refused")
	}
	if got := errCodeOf(t, e.srv, "DELETE", "/spaces/"+sp.Folder, http.StatusConflict); got != errCodeSpaceSessionsRunning {
		t.Fatalf("delete during a launch = %q, want %q", got, errCodeSpaceSessionsRunning)
	}
	if b, _ := os.ReadFile(filepath.Join(sp.Dir, "f.md")); string(b) != "x" {
		t.Fatal("a refused delete moved the space")
	}
	if n := len(spaceArchives(t, e.srv)); n != 0 {
		t.Fatalf("a refused delete left %d trash entries", n)
	}
	release()
	do(t, e.srv, "DELETE", "/spaces/"+sp.Folder, nil, http.StatusOK, nil)
}

// The same for a working copy.
func TestRepoDeleteRefusedWhileASessionIsStarting(t *testing.T) {
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

	release, ok := gitx.ReserveLaunch(dir)
	if !ok {
		t.Fatal("reservation refused")
	}
	code, raw := roundtrip(t, srv, "DELETE", "/repos/app", nil)
	if code != http.StatusConflict {
		t.Fatalf("delete during a launch = %d (%s), want 409", code, raw)
	}
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.Error.Code != errCodeSessionsRunningDelete {
		t.Fatalf("code = %q (%v), want %q", body.Error.Code, err, errCodeSessionsRunningDelete)
	}
	if !session.DirExists(dir) {
		t.Fatal("a refused delete removed the working copy")
	}
	release()
	if code, raw := roundtrip(t, srv, "DELETE", "/repos/app", nil); code != http.StatusOK {
		t.Fatalf("delete after the launch = %d (%s), want 200", code, raw)
	}
}

// A delete reads the running sessions outside the launch gate: while tmux keeps it waiting, a
// launch in another folder is reserved at once.
func TestDeleteWaitingForTmuxDoesNotHoldLaunches(t *testing.T) {
	e := spaceDeleteSetup(t)
	sp := newSpace(t, "Slow")
	bin := t.TempDir()
	started, proceed := filepath.Join(bin, "started"), filepath.Join(bin, "proceed")
	script := "#!/bin/sh\ncase \"$1\" in list-sessions) : > " + started + "; while [ ! -e " + proceed + " ]; do sleep 0.05; done;; esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = roundtrip(t, e.srv, "DELETE", "/spaces/"+sp.Folder, nil)
	}()
	for deadline := time.Now().Add(10 * time.Second); ; {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the delete never asked tmux")
		}
		time.Sleep(10 * time.Millisecond)
	}
	reserved := make(chan struct{})
	go func() {
		gitx.WithLaunchGate(func() {
			if r, ok := gitx.ReserveLaunch(filepath.Join(e.home, "elsewhere")); ok {
				r()
			}
		})
		close(reserved)
	}()
	select {
	case <-reserved:
	case <-time.After(5 * time.Second):
		t.Error("a launch waited for the delete's tmux read")
	}
	if err := os.WriteFile(proceed, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	<-done
}

// A launch in the very folder a delete is checking publishes its session and releases its
// reservation while the delete waits for its first tmux read, which then answers with the list as
// it was before the launch. The delete must not mark on that stale read: it reads again, finds
// the session running, and refuses. Both deletes, through their real handlers.
func TestDeleteReadsAgainWhenALaunchPublishedDuringItsRead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	for _, tc := range []struct {
		name string
		code string
		// setup makes the folder and answers it with the request path that deletes it.
		setup func(t *testing.T, home string) (srv *httptest.Server, dir, path string)
	}{
		{"space", errCodeSpaceSessionsRunning, func(t *testing.T, home string) (*httptest.Server, string, string) {
			e := spaceDeleteSetup(t)
			sp := newSpace(t, "Racing")
			return e.srv, sp.Dir, "/spaces/" + sp.Folder
		}},
		{"working copy", errCodeSessionsRunningDelete, func(t *testing.T, home string) (*httptest.Server, string, string) {
			t.Setenv("HOME", home)
			t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
			srv := httptest.NewServer(lockMux())
			t.Cleanup(srv.Close)
			dir := filepath.Join(home, "repos", "app")
			gitInit(t, dir)
			return srv, dir, "/repos/app"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			srv, dir, path := tc.setup(t, home)
			const racer = "racer01"
			// The first list-sessions waits for proceed and lists nothing; later ones list the
			// session once listed exists.
			bin := t.TempDir()
			first, started, proceed, listed := filepath.Join(bin, "first"), filepath.Join(bin, "started"), filepath.Join(bin, "proceed"), filepath.Join(bin, "listed")
			script := "#!/bin/sh\ncase \"$1\" in list-sessions)\n" +
				"  if [ ! -e " + first + " ]; then : > " + first + "; : > " + started + "; while [ ! -e " + proceed + " ]; do sleep 0.05; done; exit 0; fi\n" +
				"  [ -e " + listed + " ] && echo " + session.TmuxPrefix + racer + ";;\nesac\nexit 0\n"
			if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

			type answer struct {
				code int
				raw  []byte
			}
			done := make(chan answer, 1)
			go func() {
				code, raw := roundtrip(t, srv, "DELETE", path, nil)
				done <- answer{code, raw}
			}()
			for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
				if _, err := os.Stat(started); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("the delete never asked tmux")
				}
			}
			// The launch: reserve, publish the process (tmux lists it) and the meta, release.
			var release func()
			gitx.WithLaunchGate(func() {
				var ok bool
				if release, ok = gitx.ReserveLaunch(dir); !ok {
					t.Error("the launch was refused before the delete marked")
				}
			})
			if release == nil {
				t.FailNow()
			}
			if err := os.WriteFile(listed, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			session.WriteMeta(session.Meta{Name: racer, Dir: dir, Kind: session.KindShell})
			gitx.WithLaunchGate(release)
			if err := os.WriteFile(proceed, nil, 0o644); err != nil {
				t.Fatal(err)
			}

			a := <-done
			var body struct {
				Error struct{ Code string } `json:"error"`
			}
			_ = json.Unmarshal(a.raw, &body)
			if a.code != http.StatusConflict || body.Error.Code != tc.code {
				t.Fatalf("delete = %d (%s), want 409 %s", a.code, a.raw, tc.code)
			}
			if !session.DirExists(dir) {
				t.Fatal("the delete took the folder of a running session")
			}
		})
	}
}
