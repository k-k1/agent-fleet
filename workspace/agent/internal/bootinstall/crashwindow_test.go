package bootinstall

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func (f *suFixture) versions() string {
	return filepath.Join(f.home, ".local", "share", "cursor-agent", "versions")
}

// plantSwitched builds the disk a self-update leaves when it is killed after it moved the old
// tree aside and published the new one: old version 1.0.0 is at <dir>.af-old.<pid>, the new tree
// answers at the final path, and the links are given by the caller.
func (f *suFixture) plantSwitched(t *testing.T, links []cursorLinkState) cursorMarker {
	t.Helper()
	dir := filepath.Join(f.versions(), "2026.2.0-abc123")
	old := dir + ".af-old.4242"
	f.write(t, filepath.Join(old, "cursor-agent"), "#!/bin/sh\necho 1.0.0\n")
	f.write(t, filepath.Join(dir, "cursor-agent"), "#!/bin/sh\necho 2026.2.0-abc123\n")
	m := cursorMarker{Dir: dir, Old: old, Tree: true, HadTree: true, Links: links}
	if err := os.MkdirAll(filepath.Join(f.home, ".local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.env.cursorWriteMarker(m); err != nil {
		t.Fatal(err)
	}
	return m
}

func (f *suFixture) link(t *testing.T, name, target string) {
	t.Helper()
	os.Remove(f.local(name))
	if err := os.Symlink(target, f.local(name)); err != nil {
		t.Fatal(err)
	}
}

func TestCursorRecoverKillWindows(t *testing.T) {
	oldT := func(f *suFixture) string {
		return filepath.Join(f.versions(), "1.0.0", "cursor-agent")
	}
	newT := func(f *suFixture) string {
		return filepath.Join(f.versions(), "2026.2.0-abc123", "cursor-agent")
	}
	states := func(f *suFixture) []cursorLinkState {
		return []cursorLinkState{
			{Path: f.local("agent"), Kind: "symlink", Target: []byte(oldT(f))},
			{Path: f.local("cursor-agent"), Kind: "symlink", Target: []byte(oldT(f))},
		}
	}
	setup := func(t *testing.T) *suFixture {
		f := newSU(t, goodRTK, "", "", "")
		f.write(t, oldT(f), "#!/bin/sh\necho 1.0.0\n")
		return f
	}

	t.Run("killed between the alias switch and the main link switch", func(t *testing.T) {
		f := setup(t)
		f.plantSwitched(t, states(f))
		f.link(t, "agent", newT(f))
		f.link(t, "cursor-agent", oldT(f))
		if err := f.env.cursorRecover(); err != nil {
			t.Fatal(err)
		}
		for _, n := range []string{"agent", "cursor-agent"} {
			if tg, _ := os.Readlink(f.local(n)); tg != oldT(f) {
				t.Fatalf("%s -> %q, want the old tree", n, tg)
			}
		}
		if got := f.read(filepath.Join(f.versions(), "2026.2.0-abc123", "cursor-agent")); !strings.Contains(got, "1.0.0") {
			t.Fatalf("the unverified candidate stayed published: %q", got)
		}
		if _, err := os.Lstat(f.env.cursorMarkerPath()); err == nil {
			t.Fatal("marker left after a full recovery")
		}
	})
	t.Run("killed right after a regular file was moved aside", func(t *testing.T) {
		f := setup(t)
		keep := f.local("cursor-agent") + ".af-prev.4242"
		f.write(t, keep, "#!/bin/sh\necho realfile\n")
		f.plantSwitched(t, []cursorLinkState{{Path: f.local("cursor-agent"), Kind: "file", Keep: keep}})
		if err := f.env.cursorRecover(); err != nil {
			t.Fatal(err)
		}
		if got := f.read(f.local("cursor-agent")); !strings.Contains(got, "realfile") {
			t.Fatalf("the regular file did not come back: %q", got)
		}
	})
	t.Run("killed after the switch, before the stage sweep: the next start undoes the unverified switch before its latest check", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", "") // the script fails: only the recovery can bring 1.0.0 back
		f.write(t, oldT(f), "#!/bin/sh\necho 1.0.0\n")
		f.plantSwitched(t, states(f))
		f.link(t, "agent", newT(f))
		f.link(t, "cursor-agent", newT(f))
		f.env.selfUpdateCursor(context.Background())
		if out, _, err := f.env.probeArgs(f.local("cursor-agent"), nil, nil); err != nil || out != "1.0.0" {
			t.Fatalf("not rolled back: %q %v\n%s", out, err, f.out)
		}
	})
	t.Run("the pin step recovers first too", func(t *testing.T) {
		f := setup(t)
		f.plantSwitched(t, states(f))
		f.link(t, "cursor-agent", newT(f))
		f.env.Pins = map[string]string{} // no pin: nothing to install, only the recovery
		Cursor(context.Background(), f.env)
		if tg, _ := os.Readlink(f.local("cursor-agent")); tg != oldT(f) {
			t.Fatalf("link -> %q", tg)
		}
	})
	t.Run("a successful update leaves no marker and no backup", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", cursorInstaller)
		f.plantCursor(t, "1.0.0")
		f.env.selfUpdateCursor(context.Background())
		if _, err := os.Lstat(f.env.cursorMarkerPath()); err == nil {
			t.Fatal("marker left")
		}
		ents, _ := os.ReadDir(f.versions())
		for _, de := range ents {
			if strings.Contains(de.Name(), ".af-") {
				t.Fatalf("leftover %s", de.Name())
			}
		}
	})
	t.Run("a failed update rolls back through the marker and removes it", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", cursorHeader+cursorBundle(`#!/bin/sh\ncase "$0" in */versions/*) echo 2026.2.0-abc123;; *) exit 1;; esac\n`))
		f.write(t, f.local("cursor-agent"), "#!/bin/sh\necho realfile\n")
		f.env.selfUpdateCursor(context.Background())
		if got := f.read(f.local("cursor-agent")); !strings.Contains(got, "realfile") {
			t.Fatalf("not restored: %q", got)
		}
		if _, err := os.Lstat(f.env.cursorMarkerPath()); err == nil {
			t.Fatal("marker left after a clean rollback")
		}
	})
}

func TestCursorRecoverErrorsKeepTheMarkerAndNameTheOldTree(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	t.Run(".af-bad move fails", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", "")
		f.plantSwitched(t, nil)
		old := filepath.Join(f.versions(), "2026.2.0-abc123.af-old.4242")
		if err := os.Chmod(f.versions(), 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(f.versions(), 0o755) })
		err := f.env.cursorRecover()
		if err == nil || !strings.Contains(err.Error(), old) {
			t.Fatalf("error does not name the old tree: %v", err)
		}
		if _, serr := os.Lstat(f.env.cursorMarkerPath()); serr != nil {
			t.Fatal("marker dropped while the previous tree is still aside")
		}
		os.Chmod(f.versions(), 0o755)
		if err := f.env.cursorRecover(); err != nil {
			t.Fatalf("retry: %v", err)
		}
		if !strings.Contains(f.read(filepath.Join(f.versions(), "2026.2.0-abc123", "cursor-agent")), "1.0.0") {
			t.Fatal("previous tree not back")
		}
	})
	t.Run("link restore fails", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", "")
		oldTarget := filepath.Join(f.versions(), "1.0.0", "cursor-agent")
		f.plantSwitched(t, []cursorLinkState{{Path: f.local("cursor-agent"), Kind: "symlink", Target: []byte(oldTarget)}})
		f.link(t, "cursor-agent", "/elsewhere")
		bin := filepath.Dir(f.local("cursor-agent"))
		os.Chmod(bin, 0o555)
		t.Cleanup(func() { os.Chmod(bin, 0o755) })
		err := f.env.cursorRecover()
		if err == nil || !strings.Contains(err.Error(), oldTarget) {
			t.Fatalf("error does not name the previous target: %v", err)
		}
		if _, serr := os.Lstat(f.env.cursorMarkerPath()); serr != nil {
			t.Fatal("marker dropped")
		}
	})
}

func TestCursorMarkerOutsideTheCursorPathsIsRefused(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	victim := filepath.Join(f.home, "victim")
	f.write(t, filepath.Join(victim, "keep"), "x")
	if err := os.MkdirAll(filepath.Join(f.home, ".local"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, m := range []cursorMarker{
		{Dir: victim, Old: victim + ".af-old.1", Tree: true},
		{Dir: filepath.Join(f.versions(), "x"), Old: victim, Tree: true},
		{Dir: filepath.Join(f.versions(), "x"), Links: []cursorLinkState{{Path: victim, Kind: "none"}}},
	} {
		raw, _ := json.Marshal(m)
		if err := os.WriteFile(f.env.cursorMarkerPath(), raw, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := f.env.cursorRecover(); err == nil {
			t.Fatalf("marker %+v was acted on", m)
		}
		if f.read(filepath.Join(victim, "keep")) != "x" {
			t.Fatal("a path outside the cursor tree was touched")
		}
	}
}

func TestNPMLock(t *testing.T) {
	hold := func(t *testing.T, f *suFixture) *os.File {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(f.home, ".local"), 0o755); err != nil {
			t.Fatal(err)
		}
		l, err := os.OpenFile(f.env.npmLockPath(), os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Flock(int(l.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		return l
	}
	t.Run("a held lock stops the writer and the recovery; releasing it lets them run", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", "")
		tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "2.0.0")
		f.write(t, tree+npmBakSuffix+"/VERSION", "1.0.0")
		f.write(t, f.env.npmMarker(), "pending\nclaude\n")
		l := hold(t, f)
		f.env.npmRecoverAside(false)
		f.env.selfUpdateNPM()
		if got := f.read(filepath.Join(tree, "VERSION")); got != "2.0.0" {
			t.Fatalf("restored under a live writer: %q", got)
		}
		if _, err := os.Lstat(f.env.npmMarker()); err != nil {
			t.Fatal("marker removed")
		}
		if f.read(filepath.Join(f.npmDir, "npm.log")) != "" {
			t.Fatal("npm ran under a held lock")
		}
		if rc := f.env.NPMVerifyPresentStep(); rc != ExitFailed {
			t.Fatalf("verify step rc=%d under a held lock", rc)
		}
		if _, err := os.Lstat(tree + npmBakSuffix); err != nil {
			t.Fatal("backup touched under a held lock")
		}
		l.Close()
		f.env.npmRecoverAside(false)
		if got := f.read(filepath.Join(tree, "VERSION")); got != "1.0.0" {
			t.Fatalf("not restored once the lock was free: %q\n%s", got, f.out)
		}
	})
	t.Run("npm inherits the lock, so it outlives the process that started it", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", "")
		probe := filepath.Join(f.npmDir, "fd3")
		f.write(t, filepath.Join(f.npmDir, "npm"), fmt.Sprintf("#!/bin/sh\nif [ -e /proc/$$/fd/3 ]; then echo held >%q; fi\n", probe))
		e, release, ok := f.env.lockNPM()
		if !ok {
			t.Fatal("lock not taken")
		}
		defer release()
		if _, err := e.npmInstall([]string{"x"}); err != nil {
			t.Fatal(err)
		}
		if f.read(probe) != "held\n" {
			t.Fatal("npm did not get the lock's file descriptor")
		}
		if _, _, ok := f.env.lockNPM(); ok {
			t.Fatal("a second holder got the lock")
		}
	})
}

func TestCursorRecoverReviewRound1(t *testing.T) {
	t.Run("a marker that cannot be removed after success keeps the backups the rollback needs", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		f := newSU(t, goodRTK, "", "", cursorInstaller)
		// The same version is already there but does not answer (so it is replaced), and the
		// launcher is a regular file (so it is moved aside): both backups must survive.
		dir := filepath.Join(f.versions(), "2026.2.0-abc123")
		f.write(t, filepath.Join(dir, "cursor-agent"), "#!/bin/sh\nexit 1\n")
		f.write(t, f.local("cursor-agent"), "#!/bin/sh\necho realfile\n")
		local := filepath.Join(f.home, ".local")
		old := relinkRename
		relinkRename = func(a, b string) error {
			err := os.Rename(a, b)
			os.Chmod(local, 0o555) // from now on only the marker's unlink fails
			return err
		}
		t.Cleanup(func() { relinkRename = old; os.Chmod(local, 0o755) })
		f.env.selfUpdateCursor(context.Background())
		relinkRename = old
		os.Chmod(local, 0o755)
		if _, err := os.Lstat(f.env.cursorMarkerPath()); err != nil {
			t.Skip("marker was removable on this filesystem")
		}
		if strings.Contains(f.out.String(), "cursor updated:") {
			t.Fatalf("an unsettled update was reported as done:\n%s", f.out)
		}
		keep := f.local("cursor-agent") + ".af-prev." + strconv.Itoa(os.Getpid())
		if !strings.Contains(f.read(keep), "realfile") {
			t.Fatalf("the launcher backup was deleted:\n%s", f.out)
		}
		if _, err := os.Lstat(dir + ".af-old." + strconv.Itoa(os.Getpid())); err != nil {
			t.Fatalf("the previous tree backup was deleted:\n%s", f.out)
		}
		if err := f.env.cursorRecover(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(f.read(f.local("cursor-agent")), "realfile") {
			t.Fatal("launcher not restored")
		}
		if !strings.Contains(f.read(filepath.Join(dir, "cursor-agent")), "exit 1") {
			t.Fatal("previous tree not restored")
		}
	})
	t.Run("a marker with a foreign agent state or a name the reader refuses is not written or not acted on", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", "")
		os.MkdirAll(filepath.Join(f.home, ".local"), 0o755)
		if err := f.env.cursorWriteMarker(cursorMarker{Dir: filepath.Join(f.versions(), ".preview")}); err == nil {
			t.Fatal("a dot-named version was journaled")
		}
		foreign := cursorMarker{Dir: filepath.Join(f.versions(), "x"), Links: []cursorLinkState{{Path: f.local("agent"), Kind: "symlink", Target: []byte("/usr/bin/other")}}}
		if f.env.cursorMarkerValid(foreign) {
			t.Fatal("an agent state that was never cursor's was accepted")
		}
		dup := cursorMarker{Dir: filepath.Join(f.versions(), "x"), Links: []cursorLinkState{{Path: f.local("cursor-agent"), Kind: "none"}, {Path: f.local("cursor-agent"), Kind: "none"}}}
		if f.env.cursorMarkerValid(dup) {
			t.Fatal("a duplicate path was accepted")
		}
	})
	t.Run("a path that only cleans to the versions directory is refused", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", "")
		victim := filepath.Join(f.home, "victim")
		f.write(t, filepath.Join(victim, "keep"), "x")
		os.MkdirAll(filepath.Join(f.versions(), "a"), 0o755)
		dir := f.versions() + "/a/../../../../victim"
		os.MkdirAll(filepath.Join(f.home, ".local"), 0o755)
		for _, m := range []cursorMarker{{Dir: dir, Old: dir + ".af-old.1", Tree: true}, {Dir: f.versions() + "//x"}} {
			if f.env.cursorMarkerValid(m) {
				t.Fatalf("accepted %q", m.Dir)
			}
		}
	})
	t.Run("an existing .af-prev name is never overwritten", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", cursorInstaller)
		f.write(t, f.local("cursor-agent"), "#!/bin/sh\necho realfile\n")
		keep := f.local("cursor-agent") + ".af-prev." + strconv.Itoa(os.Getpid())
		f.write(t, filepath.Join(keep, "precious"), "x")
		f.env.selfUpdateCursor(context.Background())
		if f.read(filepath.Join(keep, "precious")) != "x" {
			t.Fatal("an older backup was deleted")
		}
		if !strings.Contains(f.read(f.local("cursor-agent")), "realfile") {
			t.Fatal("the launcher was changed")
		}
	})
	t.Run("a symlink target that is not UTF-8 comes back byte for byte", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", "")
		odd := filepath.Join(f.versions(), "1.0.0\xff", "cursor-agent")
		f.write(t, odd, "#!/bin/sh\necho odd\n")
		f.plantSwitched(t, []cursorLinkState{{Path: f.local("cursor-agent"), Kind: "symlink", Target: []byte(odd)}})
		f.link(t, "cursor-agent", "/elsewhere")
		if err := f.env.cursorRecover(); err != nil {
			t.Fatal(err)
		}
		if tg, _ := os.Readlink(f.local("cursor-agent")); tg != odd {
			t.Fatalf("target changed: %q", tg)
		}
	})
	t.Run("a link that was empty before is removed only while it points at the published tree", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", "")
		m := f.plantSwitched(t, []cursorLinkState{{Path: f.local("agent"), Kind: "none"}})
		f.link(t, "agent", "/usr/bin/someone-elses")
		if err := f.env.cursorRecover(); err != nil {
			t.Fatal(err)
		}
		if tg, _ := os.Readlink(f.local("agent")); tg != "/usr/bin/someone-elses" {
			t.Fatalf("a foreign agent was removed: %q", tg)
		}
		f.plantSwitched(t, []cursorLinkState{{Path: f.local("agent"), Kind: "none"}})
		f.link(t, "agent", filepath.Join(m.Dir, cursorTool))
		if err := f.env.cursorRecover(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(f.local("agent")); err == nil {
			t.Fatal("the published link stayed")
		}
	})
	t.Run("a cursor update and the pin step exclude each other", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", cursorInstaller)
		l, busy := flockPath(f.env.cursorLockPath())
		if l == nil || busy {
			t.Fatal("lock not taken")
		}
		f.env.selfUpdateCursor(context.Background())
		if rc := Cursor(context.Background(), f.env); rc != ExitFailed {
			t.Fatalf("pin step ran under a held lock: rc=%d", rc)
		}
		if usable(f.local("cursor-agent")) {
			t.Fatal("an update ran under a held lock")
		}
		l.Close()
		f.env.selfUpdateCursor(context.Background())
		if !usable(f.local("cursor-agent")) {
			t.Fatalf("no update once the lock was free:\n%s", f.out)
		}
	})
}

func TestNPMLockSurvivesAKilledParent(t *testing.T) {
	if os.Getenv("AF_TEST_NPM_PARENT") != "" {
		t.Skip("helper")
	}
	f := newSU(t, goodRTK, "", "", "")
	started := filepath.Join(f.npmDir, "started")
	release := filepath.Join(f.npmDir, "release")
	f.write(t, filepath.Join(f.npmDir, "npm"), fmt.Sprintf("#!/bin/sh\necho up >%q\nwhile [ ! -e %q ]; do /usr/bin/sleep 0.1; done\n", started, release))
	cmd := exec.Command(os.Args[0], "-test.run=^TestNPMLockHelperProcess$")
	cmd.Env = append(os.Environ(), "AF_TEST_NPM_PARENT=1", "AF_TEST_HOME="+f.home, "AF_TEST_NPM="+filepath.Join(f.npmDir, "npm"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	for i := 0; i < 100 && f.read(started) == ""; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if f.read(started) == "" {
		t.Fatal("the fake npm did not start")
	}
	cmd.Process.Kill() // only the Go process; npm runs in its own group
	cmd.Wait()
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "2.0.0")
	f.write(t, tree+npmBakSuffix+"/VERSION", "1.0.0")
	f.write(t, f.env.npmMarker(), "pending\nclaude\n")
	f.env.npmRecoverAside(false)
	if got := f.read(filepath.Join(tree, "VERSION")); got != "2.0.0" {
		t.Fatalf("restored under the surviving npm: %q", got)
	}
	os.WriteFile(release, nil, 0o644) // let the fake npm finish now that recovery was shown blocked
	for i := 0; i < 100; i++ {
		if _, rel, ok := f.env.lockNPM(); ok {
			rel()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	f.env.npmRecoverAside(false)
	if got := f.read(filepath.Join(tree, "VERSION")); got != "1.0.0" {
		t.Fatalf("not restored after npm exited: %q\n%s", got, f.out)
	}
}

func TestNPMLockHelperProcess(t *testing.T) {
	if os.Getenv("AF_TEST_NPM_PARENT") == "" {
		t.Skip("helper")
	}
	e := Env{Home: os.Getenv("AF_TEST_HOME"), NPM: os.Getenv("AF_TEST_NPM")}
	e, _, ok := e.lockNPM()
	if !ok {
		os.Exit(3)
	}
	e.npmInstall([]string{"x"})
	os.Exit(0)
}

func TestNPMLockIOErrorIsNotBusy(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newSU(t, goodRTK, "", "", "")
	local := filepath.Join(f.home, ".local")
	os.MkdirAll(local, 0o755)
	os.Chmod(local, 0o555)
	t.Cleanup(func() { os.Chmod(local, 0o755) })
	if _, _, ok := f.env.lockNPM(); !ok {
		t.Fatal("an unwritable ~/.local was reported as another holder")
	}
}
