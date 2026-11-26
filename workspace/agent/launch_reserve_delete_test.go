package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

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

	release, ok := gitx.ReserveLaunch("start01", e.home, filepath.Join(sp.Dir, "sub"))
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

	release, ok := gitx.ReserveLaunch("start02", dir)
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
