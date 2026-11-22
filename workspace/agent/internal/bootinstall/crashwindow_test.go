package bootinstall

import (
	"context"
	"encoding/json"
	"errors"
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
	hadTree := true
	hasMain := false
	for _, l := range links {
		hasMain = hasMain || l.Path == f.local("cursor-agent")
	}
	if !hasMain {
		links = append(links, cursorLinkState{Path: f.local("cursor-agent"), Kind: "none"})
	}
	m := cursorMarker{Dir: dir, Old: old, Tree: true, HadTree: &hadTree, Links: links}
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
			t.Fatalf("no marker after the update: it was not journaled, or its unlink did not fail as arranged:\n%s", f.out)
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
		l, busy, err := flockPath(f.env.cursorLockPath())
		if l == nil || busy || err != nil {
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
	f.write(t, filepath.Join(f.npmDir, "npm"), fmt.Sprintf("#!/bin/sh\necho up >%q\ni=0; while [ ! -e %q ] && [ $i -lt 300 ]; do i=$((i+1)); /usr/bin/sleep 0.1; done\n", started, release))
	t.Cleanup(func() { os.WriteFile(release, nil, 0o644) }) // a failing run must not leave the fake npm waiting
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

func TestCursorRecoverReadlinkErrorKeepsTheMarker(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newSU(t, goodRTK, "", "", "")
	m := f.plantSwitched(t, []cursorLinkState{{Path: f.local("agent"), Kind: "none"}, {Path: f.local("cursor-agent"), Kind: "none"}})
	f.link(t, "agent", filepath.Join(m.Dir, cursorTool))
	f.link(t, "cursor-agent", filepath.Join(m.Dir, cursorTool))
	bin := filepath.Dir(f.local("agent"))
	os.Chmod(bin, 0o600) // no search permission: Readlink fails with EACCES
	t.Cleanup(func() { os.Chmod(bin, 0o755) })
	if err := f.env.cursorRecover(); err == nil {
		t.Fatal("a failed look at the link was swallowed")
	}
	if _, err := os.Lstat(f.env.cursorMarkerPath()); err != nil {
		t.Fatal("marker dropped")
	}
	os.Chmod(bin, 0o755)
	if err := f.env.cursorRecover(); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if _, err := os.Lstat(f.local("agent")); err == nil {
		t.Fatal("the published agent link stayed")
	}
}

func TestCursorIncompleteMarkerLeavesALiveTree(t *testing.T) {
	no, yes := false, true
	for name, mut := range map[string]func(*cursorMarker){
		"no hadTree":         func(m *cursorMarker) { m.HadTree = nil },
		"no main link state": func(m *cursorMarker) { m.HadTree = &no; m.Links = nil },
	} {
		t.Run(name, func(t *testing.T) {
			f := newSU(t, goodRTK, "", "", "")
			live := filepath.Join(f.versions(), "live")
			f.write(t, filepath.Join(live, "cursor-agent"), "#!/bin/sh\necho live\n")
			os.MkdirAll(filepath.Dir(f.local("cursor-agent")), 0o755)
			os.MkdirAll(filepath.Join(f.home, ".local"), 0o755)
			f.link(t, "cursor-agent", filepath.Join(live, "cursor-agent"))
			m := cursorMarker{Dir: live, Old: live + ".af-old.1", Tree: true, HadTree: &yes,
				Links: []cursorLinkState{{Path: f.local("cursor-agent"), Kind: "none"}}}
			mut(&m)
			raw, _ := json.Marshal(m)
			os.WriteFile(f.env.cursorMarkerPath(), raw, 0o644)
			if err := f.env.cursorRecover(); err == nil {
				t.Fatal("an incomplete marker was acted on")
			}
			if !strings.Contains(f.read(filepath.Join(live, "cursor-agent")), "live") {
				t.Fatal("the live tree was deleted")
			}
			if tg, _ := os.Readlink(f.local("cursor-agent")); tg != filepath.Join(live, "cursor-agent") {
				t.Fatal("the live launcher was removed")
			}
		})
	}
}

func TestUnopenableLockLeavesRecoveryStateAlone(t *testing.T) {
	t.Run("npm pending and accepted backups", func(t *testing.T) {
		for _, state := range []string{"pending", "accepted"} {
			f := newSU(t, goodRTK, "", "", "")
			tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "2.0.0")
			f.write(t, tree+npmBakSuffix+"/VERSION", "1.0.0")
			f.write(t, f.env.npmMarker(), state+"\nclaude\n")
			if err := os.MkdirAll(f.env.npmLockPath(), 0o755); err != nil {
				t.Fatal(err)
			}
			f.env.npmRecoverAside(false)
			f.env.selfUpdateNPM()
			f.env.NPMVerifyPresentStep()
			if got := f.read(filepath.Join(tree, "VERSION")); got != "2.0.0" {
				t.Fatalf("%s: tree changed without the lock: %q", state, got)
			}
			if _, err := os.Lstat(tree + npmBakSuffix + "/VERSION"); err != nil {
				t.Fatalf("%s: backup touched without the lock", state)
			}
			if _, err := os.Lstat(f.env.npmMarker()); err != nil {
				t.Fatalf("%s: marker removed without the lock", state)
			}
			if f.read(filepath.Join(f.npmDir, "npm.log")) != "" {
				t.Fatalf("%s: npm ran without the lock", state)
			}
		}
	})
	t.Run("cursor journal, links and trees", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", cursorInstaller)
		m := f.plantSwitched(t, []cursorLinkState{{Path: f.local("agent"), Kind: "none"}})
		f.link(t, "agent", filepath.Join(m.Dir, cursorTool))
		f.link(t, "cursor-agent", filepath.Join(m.Dir, cursorTool))
		if err := os.MkdirAll(f.env.cursorLockPath(), 0o755); err != nil {
			t.Fatal(err)
		}
		check := func(what string) {
			t.Helper()
			if _, err := os.Lstat(f.env.cursorMarkerPath()); err != nil {
				t.Fatalf("%s: journal removed", what)
			}
			for _, p := range []string{m.Old, m.Dir} {
				if _, err := os.Lstat(p); err != nil {
					t.Fatalf("%s: %s gone", what, p)
				}
			}
			for _, l := range []string{"agent", "cursor-agent"} {
				if got, _ := os.Readlink(f.local(l)); got != filepath.Join(m.Dir, cursorTool) {
					t.Fatalf("%s: link %s moved to %q", what, l, got)
				}
			}
		}
		if rc := Cursor(context.Background(), f.env); rc != ExitFailed {
			t.Fatalf("Cursor rc=%d", rc)
		}
		check("Cursor")
		f.env.selfUpdateCursor(context.Background())
		check("selfUpdateCursor")
	})
}

func TestSelfUpdateNPMWithUnopenableLockStartsNothing(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "") // no marker: only the lock check can stop the update
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "1.0.0")
	if err := os.MkdirAll(f.env.npmLockPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	f.env.selfUpdateNPM()
	if f.read(filepath.Join(f.npmDir, "npm.log")) != "" {
		t.Fatal("npm install ran without the lock")
	}
	if got := f.read(filepath.Join(tree, "VERSION")); got != "1.0.0" {
		t.Fatalf("tree changed: %q", got)
	}
	if out, _, err := f.env.probe(f.local("claude")); err != nil || out != "1.0.0" {
		t.Fatalf("link moved: %q %v", out, err)
	}
}

// A first cursor update killed before it published its tree, then the user installs that same
// version by hand: recovery must neither delete the user's tree nor its launcher.
func TestCursorRecoverLeavesAForeignTreeAndItsLauncher(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	dir := filepath.Join(f.versions(), "2026.2.0-abc123")
	f.write(t, filepath.Join(dir, "cursor-agent"), "#!/bin/sh\necho manual\n")
	os.MkdirAll(filepath.Join(f.home, ".local", "bin"), 0o755)
	f.link(t, "cursor-agent", filepath.Join(dir, "cursor-agent"))
	no := false
	m := cursorMarker{Dir: dir, Old: dir + ".af-old.1", Tree: true, HadTree: &no, ID: "staged-token",
		Links: []cursorLinkState{{Path: f.local("cursor-agent"), Kind: "none"}}}
	if err := f.env.cursorWriteMarker(m); err != nil {
		t.Fatal(err)
	}
	if err := f.env.cursorRecover(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.read(filepath.Join(dir, "cursor-agent")), "manual") {
		t.Fatal("the user's tree was deleted")
	}
	if out, err := exec.Command(f.local("cursor-agent")).Output(); err != nil || !strings.Contains(string(out), "manual") {
		t.Fatalf("the user's launcher no longer starts their tree: %v %q", err, out)
	}
	if _, err := os.Lstat(f.env.cursorMarkerPath()); err == nil {
		t.Fatal("the marker stayed")
	}
	// The same marker with the tree's own token does delete it.
	f.write(t, filepath.Join(dir, cursorIDFile), "staged-token")
	if err := f.env.cursorWriteMarker(m); err != nil {
		t.Fatal(err)
	}
	if err := f.env.cursorRecover(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dir); err == nil {
		t.Fatal("the staged tree stayed")
	}
}

// A real interrupted update journals the token, and it is the staged tree's own file (so it
// survives the rename and a remount, unlike an inode).
func TestCursorMarkerCarriesTheStagedTreeToken(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	dir := filepath.Join(f.versions(), "v")
	f.write(t, filepath.Join(dir, "cursor-agent"), "x")
	id, err := stampTree(dir)
	if err != nil || id == "" {
		t.Fatal(err)
	}
	moved := dir + "-moved"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	m := cursorMarker{ID: id}
	own := func(m cursorMarker, p string) string { o, _ := m.dirOwner(p); return o }
	if own(m, moved) != "ours" || own(m, f.versions()) != "foreign" {
		t.Fatal("the token does not identify the staged tree")
	}
	if own(cursorMarker{}, f.versions()) != "ours" {
		t.Fatal("a marker without a token must keep the old behaviour")
	}
}

// Only the supervisor holds the lock here (npm closes its copy at once and the caller drops its
// own), and it is still refused to a second holder until npm and its straggler are gone.
func TestNPMSupervisorHoldsTheLockForTheGroup(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	ready, release := filepath.Join(f.home, "ready"), filepath.Join(f.home, "release")
	pidf := filepath.Join(f.home, "straggler.pid")
	f.write(t, filepath.Join(f.npmDir, "npm"), fmt.Sprintf(
		"#!/bin/sh\nexec 3>&-\n(exec /bin/sleep 30) &\necho $! >%q\n: >%q\nwhile [ ! -e %q ]; do /bin/sleep 0.05; done\n", pidf, ready, release))
	e, rel, ok := f.env.lockNPM()
	if !ok {
		t.Fatal("lock not taken")
	}
	done := make(chan error, 1)
	go func() {
		se, err := e.npmInstall([]string{"x"})
		if err != nil {
			err = fmt.Errorf("%v: %s", err, se)
		}
		done <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for !exists(ready) {
		if time.Now().After(deadline) {
			t.Fatal("npm never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	rel() // the caller's own hold goes, as a killed caller's would
	e.npmLockFile.Close()
	if _, _, busy := probeLock(f.env.npmLockPath()); !busy {
		t.Fatal("the lock was free while npm's group still ran")
	}
	os.WriteFile(release, nil, 0o644)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, _, busy := probeLock(f.env.npmLockPath()); busy {
		t.Fatal("the lock stayed after the group was gone")
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(f.read(pidf)))
	if pid > 0 {
		if b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); len(b) > 0 && !strings.Contains(string(b), ") Z") {
			t.Fatal("npm's straggler outlived the install")
		}
	}
}

// A missing npm and a failing npm are both reported from the supervisor as main reports them.
func TestNPMSupervisorReportsStartErrorsAndExitCodes(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	e, rel, ok := f.env.lockNPM()
	if !ok {
		t.Fatal("lock not taken")
	}
	defer rel()
	good := e.NPM
	e.NPM = filepath.Join(f.home, "no-such-npm")
	se, err := e.npmInstall([]string{"x"})
	if err == nil || !strings.Contains(se, "no-such-npm") {
		t.Fatalf("start error lost: %v %q", err, se)
	}
	e.NPM = good
	f.write(t, filepath.Join(f.npmDir, "npm"), "#!/bin/sh\necho boom >&2\nexit 7\n")
	se, err = e.npmInstall([]string{"x"})
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 7 || !strings.Contains(se, "boom") {
		t.Fatalf("exit code or stderr lost: %v %q", err, se)
	}
}

func probeLock(path string) (*os.File, bool, bool) {
	fl, busy, _ := flockPath(path)
	if fl != nil {
		fl.Close()
	}
	return fl, false, busy
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// After a foreign tree is found, a regular file this update moved aside still goes back to its
// empty place, and never over something that is there.
func TestCursorRecoverForeignTreeStillRestoresAMovedAsideFile(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	dir := filepath.Join(f.versions(), "2026.2.0-abc123")
	f.write(t, filepath.Join(dir, "cursor-agent"), "#!/bin/sh\necho restored-tree\n") // no token
	os.MkdirAll(filepath.Join(f.home, ".local", "bin"), 0o755)
	keep := f.local("cursor-agent") + ".af-prev.9"
	f.write(t, keep, "#!/bin/sh\necho realfile\n")
	yes := true
	m := cursorMarker{Dir: dir, Old: dir + ".af-old.9", Tree: true, HadTree: &yes, ID: "t",
		Links: []cursorLinkState{{Path: f.local("cursor-agent"), Kind: "file", Keep: keep}}}
	if err := f.env.cursorWriteMarker(m); err != nil {
		t.Fatal(err)
	}
	if err := f.env.cursorRecover(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.read(f.local("cursor-agent")), "realfile") {
		t.Fatal("the moved-aside launcher did not come back")
	}
}

// A real update stamps the tree it publishes, and the stamp is what the marker is matched to.
func TestSelfUpdateCursorStampsThePublishedTree(t *testing.T) {
	f := newSU(t, goodRTK, "", "", cursorInstaller)
	f.env.selfUpdateCursor(context.Background())
	b, err := os.ReadFile(filepath.Join(f.versions(), "2026.2.0-abc123", cursorIDFile))
	if err != nil || len(b) != 32 {
		t.Fatalf("no token in the published tree: %v %q", err, b)
	}
}

// A link could not be put back (an unwritable ~/.local/bin): the tree is left as the candidate
// with its token, so the retry still owns it and fixes the link, then restores the tree.
func TestCursorRecoverRetriesLinksBeforeRestoringTheTree(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	dir := filepath.Join(f.versions(), "2026.2.0-abc123")
	old := dir + ".af-old.4242"
	prev := filepath.Join(f.versions(), "1.0.0", "cursor-agent")
	f.write(t, prev, "#!/bin/sh\necho 1.0.0\n")
	f.write(t, filepath.Join(old, "cursor-agent"), "#!/bin/sh\necho 1.0.0-slot\n")
	f.write(t, filepath.Join(dir, "cursor-agent"), "#!/bin/sh\necho cand\n")
	f.write(t, filepath.Join(dir, cursorIDFile), "tok")
	os.MkdirAll(filepath.Dir(f.local("agent")), 0o755)
	f.link(t, "agent", filepath.Join(dir, "cursor-agent"))
	yes := true
	m := cursorMarker{Dir: dir, Old: old, Tree: true, HadTree: &yes, ID: "tok", Links: []cursorLinkState{
		{Path: f.local("agent"), Kind: "symlink", Target: []byte(prev)},
		{Path: f.local("cursor-agent"), Kind: "none"}}}
	if err := f.env.cursorWriteMarker(m); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Dir(f.local("agent"))
	os.Chmod(bin, 0o555)
	defer os.Chmod(bin, 0o755)
	if err := f.env.cursorRecover(); err == nil {
		t.Skip("the directory stayed writable (running as root?)")
	}
	os.Chmod(bin, 0o755)
	if err := f.env.cursorRecover(); err != nil {
		t.Fatal(err)
	}
	if tg, _ := os.Readlink(f.local("agent")); tg != prev {
		t.Fatalf("agent -> %q, want %q", tg, prev)
	}
	if !strings.Contains(f.read(filepath.Join(dir, "cursor-agent")), "1.0.0-slot") {
		t.Fatal("the previous tree was not restored on the retry")
	}
}

// A moved-aside directory (any kind of file, not only a regular one) goes back to an empty place
// beside a foreign tree, and a place that is in use is left alone.
func TestCursorRecoverForeignTreeRestoresAKeptDirectoryNotOverAnything(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	dir := filepath.Join(f.versions(), "2026.2.0-abc123")
	f.write(t, filepath.Join(dir, "cursor-agent"), "manual")
	os.MkdirAll(filepath.Dir(f.local("cursor-agent")), 0o755)
	keep := f.local("cursor-agent") + ".af-prev.3"
	f.write(t, filepath.Join(keep, "inner"), "kept dir")
	yes := true
	m := cursorMarker{Dir: dir, Old: dir + ".af-old.3", Tree: true, HadTree: &yes, ID: "tok",
		Links: []cursorLinkState{{Path: f.local("cursor-agent"), Kind: "file", Keep: keep}}}
	if err := f.env.cursorWriteMarker(m); err != nil {
		t.Fatal(err)
	}
	if err := f.env.cursorRecover(); err != nil {
		t.Fatal(err)
	}
	if f.read(filepath.Join(f.local("cursor-agent"), "inner")) != "kept dir" {
		t.Fatal("the directory did not come back")
	}
	// In use: the manual installer's launcher stays, the kept item stays too.
	os.RemoveAll(f.local("cursor-agent"))
	f.write(t, f.local("cursor-agent"), "mine")
	f.write(t, filepath.Join(keep, "inner"), "kept dir")
	if err := f.env.cursorWriteMarker(m); err != nil {
		t.Fatal(err)
	}
	if err := f.env.cursorRecover(); err != nil {
		t.Fatal(err)
	}
	if f.read(f.local("cursor-agent")) != "mine" {
		t.Fatal("a launcher in use was overwritten")
	}
}

// An installer's read-only (0555) staged tree still gets its token, and keeps its mode.
func TestStampTreeKeepsAReadOnlyRoot(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tree")
	os.Mkdir(p, 0o755)
	os.Chmod(p, 0o555)
	defer os.Chmod(p, 0o755)
	if _, err := stampTree(p); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode() != os.ModeDir|0o555 {
		t.Fatalf("mode %v", fi.Mode())
	}
	// setgid survives too.
	q := filepath.Join(t.TempDir(), "sg")
	os.Mkdir(q, 0o755)
	os.Chmod(q, 0o555|os.ModeSetgid)
	defer os.Chmod(q, 0o755)
	want, _ := os.Stat(q)
	if _, err := stampTree(q); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Stat(q); got.Mode() != want.Mode() {
		t.Fatalf("mode %v, want %v", got.Mode(), want.Mode())
	}
}

// The slot at Dir is a regular file (the update died before moving it): recovery settles and
// leaves it, it does not wait on a token it cannot read.
func TestCursorRecoverSlotIsARegularFile(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	dir := filepath.Join(f.versions(), "2026.2.0-abc123")
	f.write(t, dir, "file in the slot")
	yes := true
	m := cursorMarker{Dir: dir, Old: dir + ".af-old.5", Tree: true, HadTree: &yes, ID: "tok",
		Links: []cursorLinkState{{Path: f.local("cursor-agent"), Kind: "none"}}}
	os.MkdirAll(filepath.Dir(f.local("cursor-agent")), 0o755)
	if err := f.env.cursorWriteMarker(m); err != nil {
		t.Fatal(err)
	}
	if err := f.env.cursorRecover(); err != nil {
		t.Fatal(err)
	}
	if f.read(dir) != "file in the slot" {
		t.Fatal("the slot was changed")
	}
}

// A filesystem that refuses RENAME_NOREPLACE (EINVAL on NFS/EFS): a kept regular file still goes
// back through a hard link, a kept directory stays put without failing the recovery, and a place
// in use is never overwritten.
func TestCursorRecoverForeignTreeWithoutNoReplaceRename(t *testing.T) {
	old := renameNoReplace
	renameNoReplace = func(string, string) error { return syscall.EINVAL }
	defer func() { renameNoReplace = old }()
	f := newSU(t, goodRTK, "", "", "")
	dir := filepath.Join(f.versions(), "2026.2.0-abc123")
	f.write(t, filepath.Join(dir, "cursor-agent"), "manual")
	os.MkdirAll(filepath.Dir(f.local("cursor-agent")), 0o755)
	keep := f.local("cursor-agent") + ".af-prev.3"
	yes := true
	m := cursorMarker{Dir: dir, Old: dir + ".af-old.3", Tree: true, HadTree: &yes, ID: "tok",
		Links: []cursorLinkState{{Path: f.local("cursor-agent"), Kind: "file", Keep: keep}}}
	run := func() {
		t.Helper()
		if err := f.env.cursorWriteMarker(m); err != nil {
			t.Fatal(err)
		}
		if err := f.env.cursorRecover(); err != nil {
			t.Fatal(err)
		}
	}
	f.write(t, keep, "realfile")
	run()
	if f.read(f.local("cursor-agent")) != "realfile" {
		t.Fatal("the file did not come back through the fallback")
	}
	os.Remove(f.local("cursor-agent"))
	f.write(t, filepath.Join(keep, "inner"), "kept dir")
	os.Remove(keep)
	f.write(t, filepath.Join(keep, "inner"), "kept dir")
	run()
	if f.read(filepath.Join(keep, "inner")) != "kept dir" {
		t.Fatal("the kept directory was not preserved")
	}
	f.write(t, f.local("cursor-agent"), "mine")
	os.RemoveAll(keep)
	f.write(t, keep, "realfile")
	run()
	if f.read(f.local("cursor-agent")) != "mine" {
		t.Fatal("a place in use was overwritten")
	}
}
