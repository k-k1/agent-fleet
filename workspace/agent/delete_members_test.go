package main

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/spacex"
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
	// Restoring the space takes them off the shelf by the same rule.
	do(t, e.srv, "POST", "/cleanup/archives/"+out.Archive+"/restore", nil, http.StatusOK, nil)
	for _, n := range []string{"aihome1", "ailink1"} {
		if m, _ := session.ReadMeta(n); m.Archived {
			t.Fatalf("%s is still on the shelf after the space came back", n)
		}
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

// After the move the stopped sessions are settled as their meta is now, and only while they still
// live in the folder: one whose link was re-pointed elsewhere meanwhile stays where it is.
func TestSettleAfterTheMoveLeavesASessionMovedElsewhere(t *testing.T) {
	e := spaceDeleteSetup(t)
	gone := filepath.Join(e.home, "gone")
	elsewhere := filepath.Join(e.home, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(e.home, "alias")
	if err := os.Symlink(gone, link); err != nil { // dangles, as once the folder is moved
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "aistay1", Dir: link, Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "aimove1", Dir: elsewhere, Kind: session.KindClaude})
	tmuxSays(t, tmuxNoServer)
	gitx.ShelveSessionsUnder(gone, []session.Meta{{Name: "aistay1"}, {Name: "aimove1"}})
	if m, _ := session.ReadMeta("aistay1"); !m.Archived {
		t.Fatal("a session reaching the removed folder through a dangling link was not shelved")
	}
	if m, _ := session.ReadMeta("aimove1"); m.Archived {
		t.Fatal("a session that no longer lives in the folder was shelved")
	}
}

// tmuxCountdown answers list-sessions with no server for the first ok calls and fails the rest;
// every other tmux call says there is no server.
func tmuxCountdown(t *testing.T, ok int) {
	t.Helper()
	n := filepath.Join(t.TempDir(), "n")
	tmuxSays(t, `case "$1" in
list-sessions)
  c=$(cat '`+n+`' 2>/dev/null || echo 0); c=$((c+1)); echo $c > '`+n+`'
  if [ $c -le `+strconv.Itoa(ok)+` ]; then echo 'no server running on /tmp/x' >&2; exit 1; fi
  echo 'error connecting to /tmp/x (Permission denied)' >&2; exit 1;;
esac
echo 'no server running on /tmp/x' >&2
exit 1
`)
}

// A delete reads its sessions twice before it changes anything (the decision, and the set it
// settles once marked); a failure of the second read refuses as well, with nothing changed. A read
// after the move that fails still settles the sessions decided before it.
func TestDeleteSpaceObservesEveryReadBeforeTheMove(t *testing.T) {
	e := spaceDeleteSetup(t)
	sp := newSpace(t, "Counted")
	writeAt(t, filepath.Join(sp.Dir, "f.md"), "x")
	session.WriteMeta(session.Meta{Name: "aicnt01", Dir: sp.Dir, Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "shcnt01", Dir: sp.Dir, Kind: session.KindShell})

	tmuxCountdown(t, 1)
	if got := errCodeOf(t, e.srv, "DELETE", "/spaces/"+sp.Folder, http.StatusServiceUnavailable); got != errCodeSpaceSessionsUnobservable {
		t.Fatalf("second read failing = %q", got)
	}
	if b, _ := os.ReadFile(filepath.Join(sp.Dir, "f.md")); string(b) != "x" || len(spaceArchives(t, e.srv)) != 0 {
		t.Fatal("a refused delete changed the space")
	}
	if _, ok := session.ReadMeta("shcnt01"); !ok {
		t.Fatal("a refused delete trashed the shell")
	}

	tmuxCountdown(t, 2)
	do(t, e.srv, "DELETE", "/spaces/"+sp.Folder, nil, http.StatusOK, nil)
	if m, _ := session.ReadMeta("aicnt01"); !m.Archived {
		t.Fatal("the read after the move failed and the decided AI session was left listed")
	}
	if _, ok := session.ReadMeta("shcnt01"); ok {
		t.Fatal("the shell was not trashed")
	}
}

// The working copy's delete reads the same way.
func TestDeleteRepoObservesEveryReadBeforeTheRemoval(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	e := spaceDeleteSetup(t)
	dir := filepath.Join(e.home, "repos", "app")
	gitInit(t, dir)
	session.WriteMeta(session.Meta{Name: "aicnt02", Dir: dir, Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "shcnt02", Dir: dir, Kind: session.KindShell})

	tmuxCountdown(t, 1)
	if got := errCodeOf(t, e.srv, "DELETE", "/repos/app", http.StatusServiceUnavailable); got != errCodeSessionsUnobservable {
		t.Fatalf("second read failing = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("the working copy was removed: %v", err)
	}
	if _, ok := session.ReadMeta("shcnt02"); !ok {
		t.Fatal("a refused delete trashed the shell")
	}

	tmuxCountdown(t, 2)
	do(t, e.srv, "DELETE", "/repos/app", nil, http.StatusOK, nil)
	if m, _ := session.ReadMeta("aicnt02"); !m.Archived {
		t.Fatal("the read after the removal failed and the decided AI session was left listed")
	}
}

// A sessions directory that cannot be listed refuses the delete.
func TestDeleteSpaceRefusesAnUnlistableSessionsDirectory(t *testing.T) {
	e := spaceDeleteSetup(t)
	sp := newSpace(t, "Unlisted")
	tmuxSays(t, tmuxNoServer)
	notDir := filepath.Join(e.home, "sessions-file")
	if err := os.WriteFile(notDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_SESSIONS_DIR", notDir)
	if got := errCodeOf(t, e.srv, "DELETE", "/spaces/"+sp.Folder, http.StatusServiceUnavailable); got != errCodeSpaceSessionsUnobservable {
		t.Fatalf("unlistable sessions = %q", got)
	}
	if _, ok := spacex.Lookup(sp.Folder); !ok {
		t.Fatal("the space was moved")
	}
}

// A session started through a link that preceded `..`, and re-pointed since, still runs in the
// folder its process entered: the guards count the lexical clean, so the delete is refused.
func TestDeleteRepoRefusesASessionStartedThroughALinkRepointedSince(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	e := spaceDeleteSetup(t)
	repos := filepath.Join(e.home, "repos")
	dir := filepath.Join(repos, "app")
	gitInit(t, dir)
	peer := filepath.Join(e.home, "other", "peer")
	if err := os.MkdirAll(filepath.Join(e.home, "other", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(peer, 0o755); err != nil {
		t.Fatal(err)
	}
	// Started while jump pointed at repos/peer, so the process entered repos/app; re-pointed since.
	if err := os.Symlink(peer, filepath.Join(repos, "jump")); err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "shjump1", Dir: filepath.Join(repos, "jump") + "/../app", Kind: session.KindShell})
	tmuxSays(t, "case \"$1\" in list-sessions) echo "+session.TmuxPrefix+"shjump1; exit 0;; esac\necho 'no server running on /tmp/x' >&2\nexit 1\n")
	if got := errCodeOf(t, e.srv, "DELETE", "/repos/app", http.StatusConflict); got != errCodeSessionsRunningDelete {
		t.Fatalf("running session through a re-pointed link = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("the working copy was removed: %v", err)
	}
}

// A client that sent the sessions it showed (expect) is held to them: one the Agent finds in the
// space and the client did not name is reported, and nothing is changed until it is named.
func TestDeleteSpaceRefusesSessionsTheClientDidNotShow(t *testing.T) {
	e := spaceDeleteSetup(t)
	sp := newSpace(t, "Shown")
	link := filepath.Join(e.home, "alias")
	if err := os.Symlink(sp.Dir, link); err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "aishow1", Dir: sp.Dir, Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "shhide1", Dir: link, Kind: session.KindShell})
	tmuxSays(t, tmuxNoServer)

	var body struct {
		Error struct {
			Code     string
			Sessions []string
		}
	}
	do(t, e.srv, "DELETE", "/spaces/"+sp.Folder+"?expect=aishow1", nil, http.StatusConflict, &body)
	if body.Error.Code != errCodeSpaceSessionsChanged || strings.Join(body.Error.Sessions, ",") != "shhide1" {
		t.Fatalf("answer = %+v", body)
	}
	if _, ok := session.ReadMeta("shhide1"); !ok || len(spaceArchives(t, e.srv)) != 0 {
		t.Fatal("a refused delete changed something")
	}
	do(t, e.srv, "DELETE", "/spaces/"+sp.Folder+"?expect=aishow1,shhide1", nil, http.StatusOK, nil)
	if _, ok := session.ReadMeta("shhide1"); ok {
		t.Fatal("the shown shell was not trashed")
	}
}

// A worktree's delete shelves a session started at home with its subdir in the worktree, and one
// reached through a link, records both in the tombstone, and its restore takes both off the shelf.
func TestDeletedWorktreeRestoresEverySpellingOfItsSessions(t *testing.T) {
	e := tombstoneSetup(t)
	link := filepath.Join(e.home, "alias")
	if err := os.Symlink(e.wt, link); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(e.home, filepath.Join(e.wt, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "aihome3", Dir: e.home, Subdir: rel, Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "ailink3", Dir: link, Kind: session.KindClaude})
	tmuxSays(t, tmuxNoServer)

	do(t, e.srv, "DELETE", "/repos/app@wt-x?force=true", nil, http.StatusOK, nil)
	arcs := worktreeArchives(t, e.srv)
	if len(arcs) != 1 || arcs[0].Worktree == nil {
		t.Fatalf("archives = %+v", arcs)
	}
	shelved := strings.Join(arcs[0].Worktree.Shelved, ",")
	for _, n := range []string{"aihome3", "ailink3"} {
		if !strings.Contains(shelved, n) {
			t.Fatalf("tombstone shelf list %q misses %s", shelved, n)
		}
		if m, _ := session.ReadMeta(n); !m.Archived {
			t.Fatalf("%s was not shelved", n)
		}
	}
	do(t, e.srv, "POST", "/cleanup/archives/"+arcs[0].ID+"/restore", nil, http.StatusOK, nil)
	for _, n := range []string{"aihome3", "ailink3", "aiaaaa1", "aisub01"} {
		if m, _ := session.ReadMeta(n); m.Archived {
			t.Fatalf("%s is still on the shelf after the worktree came back", n)
		}
	}
}

// The set a worktree's delete settles is read once its status is checked and its tombstone is
// prepared, as late as the shelf list allows: a session taken off the shelf before then is shelved
// and recorded, so the restore brings it back too.
func TestWorktreeDeleteReadsItsSessionsAfterTheStatus(t *testing.T) {
	e := tombstoneSetup(t)
	session.WriteMeta(session.Meta{Name: "aiback1", Dir: e.wt, Kind: session.KindClaude, Archived: true})
	tmuxSays(t, tmuxNoServer)
	restore := gitx.SetDeleteStepHook(func(step, dir string) {
		if step == "tombstone" {
			sessionx.UpdateSessionMeta("aiback1", func(m *session.Meta) bool { m.Archived = false; return true })
		}
	})
	defer restore()
	do(t, e.srv, "DELETE", "/repos/app@wt-x?force=true", nil, http.StatusOK, nil)
	arcs := worktreeArchives(t, e.srv)
	if len(arcs) != 1 || arcs[0].Worktree == nil || !strings.Contains(strings.Join(arcs[0].Worktree.Shelved, ","), "aiback1") {
		t.Fatalf("the session taken off the shelf before the tombstone is not in it: %+v", arcs)
	}
	if m, _ := session.ReadMeta("aiback1"); !m.Archived {
		t.Fatal("the session was not shelved")
	}
}
