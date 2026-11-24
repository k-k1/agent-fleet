package bootinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// startSleeper starts a child the test owns and returns it with its identity.
func startSleeper(t *testing.T) (*exec.Cmd, npmProc) {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	st, ok := readStat(cmd.Process.Pid)
	if !ok || st.start == 0 {
		t.Fatalf("no stat for the sleeper: %+v", st)
	}
	return cmd, npmProc{cmd.Process.Pid, st.start}
}

// A recorded process that still runs refuses the lock (to a new holder and to one that already
// has it) the way an unopenable lock file does, so recovery restores nothing under it; once it is
// gone the record goes and recovery proceeds.
func TestNPMStuckProcessRefusesTheLockUntilItIsGone(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "2.0.0")
	f.write(t, tree+npmBakSuffix+"/VERSION", "1.0.0")
	f.write(t, f.env.npmMarker(), "pending\nclaude\n")
	held, rel := lockedEnv(t, f)
	sleeper, p := startSleeper(t)
	if runs, err := held.npmRecordStuck([]npmProc{p, {p.pid, p.start + 1}}); !runs || err != nil {
		t.Fatalf("record: %v %v", runs, err)
	}
	if b := f.read(f.env.npmStuckPath()); strings.Count(b, "\n") != 1 || !strings.HasPrefix(b, bootID()+" ") {
		t.Fatalf("the record names something other than the running process: %q", b)
	}
	if _, _, ok := held.lockNPM(); ok {
		t.Fatal("a holder that already had the lock kept it while a stuck process runs")
	}
	rel()
	e, _, ok := f.env.lockNPM()
	if ok || !errors.Is(e.npmLockErr, errNPMStuck) {
		t.Fatalf("the lock was granted while a stuck process runs: %v", e.npmLockErr)
	}
	f.env.npmRecoverAside(false)
	if got := f.read(filepath.Join(tree, "VERSION")); got != "2.0.0" {
		t.Fatalf("restored under a stuck process: %q", got)
	}
	if !strings.Contains(f.out.String(), "did not stop after SIGKILL") {
		t.Fatalf("the refusal does not say why:\n%s", f.out)
	}
	_ = sleeper.Process.Kill()
	_ = sleeper.Wait()
	f.env.npmRecoverAside(false)
	if got := f.read(filepath.Join(tree, "VERSION")); got != "1.0.0" {
		t.Fatalf("not restored once the process was gone: %q\n%s", got, f.out)
	}
	if exists(f.env.npmStuckPath()) {
		t.Fatal("the record of a process that is gone stayed")
	}
}

// Only a process that still runs as the one recorded counts: another start time (a reused pid),
// another boot, a zombie, or junk in the file name nothing.
func TestNPMStuckRecordNamesOnlyTheSameRunningProcess(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	_, p := startSleeper(t)
	if runs, _ := f.env.npmRecordStuck([]npmProc{{p.pid, p.start + 1}}); runs || exists(f.env.npmStuckPath()) {
		t.Fatal("a reused pid was recorded")
	}
	os.MkdirAll(filepath.Join(f.home, ".local"), 0o755)
	f.write(t, f.env.npmStuckPath(), fmt.Sprintf("another-boot %d %d\njunk\n%s x 1\n", p.pid, p.start, bootID()))
	if err := f.env.npmStuckCheck(); err != nil {
		t.Fatalf("an entry of another boot or junk refused the lock: %v", err)
	}
	if exists(f.env.npmStuckPath()) {
		t.Fatal("a record naming nothing that runs stayed")
	}
	z, zp := startSleeper(t)
	_ = z.Process.Kill()
	waitFor(t, "the sleeper never exited", func() bool { st, ok := readStat(zp.pid); return ok && !st.live })
	if procRuns(zp) {
		t.Fatal("an unreaped zombie counts as running")
	}
	if !procRuns(npmProc{os.Getpid(), mustStart(t, os.Getpid())}) {
		t.Fatal("this process does not count as running")
	}
}

func mustStart(t *testing.T, pid int) uint64 {
	t.Helper()
	st, ok := readStat(pid)
	if !ok {
		t.Fatal("no stat")
	}
	return st.start
}

// An unreadable record is not "nothing runs".
func TestNPMStuckUnreadableRecordRefuses(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	if err := os.MkdirAll(f.env.npmStuckPath(), 0o755); err != nil { // a directory: read fails with EISDIR
		t.Fatal(err)
	}
	if err := f.env.npmStuckCheck(); !errors.Is(err, errNPMStuck) {
		t.Fatalf("an unreadable record let the lock through: %v", err)
	}
}

// The supervisor names its children that are still there, with their start times, and the caller
// reads exactly those lines back.
func TestSupervisorReportsItsStuckChildren(t *testing.T) {
	_, p := startSleeper(t)
	var b bytes.Buffer
	reportStuck(&b)
	got := parseStuck("npm noise\n" + b.String() + "stuck 1 x\nstuck 0 5\n  stuck 7 7\n")
	found := false
	for _, g := range got {
		found = found || g == p
	}
	if !found || len(got) != strings.Count(b.String(), "\n") {
		t.Fatalf("report %q parsed as %v, want %v among them and nothing else", b.String(), got, p)
	}
}

// npm's group outlives the SIGKILL (the seam stands in for a write blocked in uninterruptible
// I/O, which no test can produce): the install fails with errNPMStuck, the members are recorded,
// and the lock is refused until they are gone.
func TestNPMInstallRecordsAGroupThatOutlivesTheKill(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	n := newFakeNPM(t, f, shStraggler, "exec /bin/sleep {TOKEN}\n")
	e, rel := lockedEnv(t, f)
	e.npmGroupKill = func(int) {}
	e.npmGrace = 100 * time.Millisecond
	in := startInstall(t, f, e, rel, n)
	if err := in.sup.Kill(); err != nil { // the supervisor alone: npm and its straggler stay
		t.Fatal(err)
	}
	if err := in.wait(t); !errors.Is(err, errNPMStuck) {
		t.Fatalf("got %v, want errNPMStuck", err)
	}
	rel()
	if lockBusy(f) {
		t.Fatal("test setup: the flock itself is still held")
	}
	if _, _, ok := f.env.lockNPM(); ok {
		t.Fatal("the lock was granted while npm's group still runs")
	}
	n.kill(f, n.strayPid)
	n.kill(f, n.npmPid)
	waitFor(t, "npm's group never went", func() bool { return !n.runs(t, f, n.strayPid) && !n.runs(t, f, n.npmPid) })
	_, release, ok := f.env.lockNPM()
	if !ok {
		t.Fatal("the lock stayed refused after the group was gone")
	}
	release()
}

// A self-update whose install leaves a process running puts nothing back: the backups and the
// pending marker stay for a start that finds the process gone, whose recovery restores them.
func TestSelfUpdateNPMLeavesTheBackupsWhileAWriterIsStuck(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "1.0.0")
	n := newFakeNPM(t, f, shStraggler, "exec /bin/sleep {TOKEN}\n")
	npm := filepath.Join(f.npmDir, "npm")
	body := f.read(npm)
	// ls and view answer nothing at once, so the update decides to install.
	f.write(t, npm, strings.Replace(body, "#!/bin/sh\n", "#!/bin/sh\n[ \"$1\" = install ] || exit 0\n", 1))
	e := f.env
	e.npmGroupKill = func(int) {}
	e.npmGrace = 100 * time.Millisecond
	e.afterNPMStart = func(p *os.Process) {
		waitFor(t, "npm never got ready", func() bool { return exists(n.ready) })
		_ = p.Kill()
	}
	e.selfUpdateNPM()
	if got := f.read(filepath.Join(tree+npmBakSuffix, "VERSION")); got != "1.0.0" {
		t.Fatalf("the previous copy was put back under a running writer: %q\n%s", got, f.out)
	}
	if state, _, ok, _ := f.env.npmReadMarker(); !ok || state != "pending" {
		t.Fatalf("marker %q %v, want pending", state, ok)
	}
	if !strings.Contains(f.out.String(), "until a later start restores them") {
		t.Fatalf("no warning:\n%s", f.out)
	}
	n.kill(f, n.strayPid)
	n.kill(f, n.npmPid)
	waitFor(t, "npm's group never went", func() bool { return !n.runs(t, f, n.strayPid) && !n.runs(t, f, n.npmPid) })
	f.env.npmRecoverAside(false)
	if got := f.read(filepath.Join(tree, "VERSION")); got != "1.0.0" {
		t.Fatalf("not restored once the writer was gone: %q\n%s", got, f.out)
	}
}

// The every-start pass whose first lock attempt fails and whose verify pass then gets the lock
// settles the accepted update's backups in the same start.
func TestNPMVerifyPresentSettlesBackupsWhenAnInnerRetryGetsTheLock(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "2.0.0")
	f.write(t, tree+npmBakSuffix+"/VERSION", "1.0.0")
	f.write(t, f.env.npmMarker(), "accepted\nclaude\n")
	// The first two attempts (the step's own and the keep-accepted recovery's) find the lock path
	// unopenable; it is fixed before the verify pass asks.
	lock := f.env.npmLockPath()
	if err := os.MkdirAll(lock, 0o755); err != nil {
		t.Fatal(err)
	}
	tries := 0
	e := f.env
	e.beforeNPMLock = func() {
		if tries++; tries == 3 {
			os.Remove(lock)
		}
	}
	if rc := e.NPMVerifyPresentStep(); rc != ExitOK {
		t.Fatalf("rc %d\n%s", rc, f.out)
	}
	if exists(tree + npmBakSuffix) {
		t.Fatalf("the accepted backup waited for the next start\n%s", f.out)
	}
	if exists(f.env.npmMarker()) {
		t.Fatal("the marker stayed")
	}
}

// With --repin and no local copy at all, the pinned install runs no `npm ls`.
func TestNPMRepinWithoutLocalCopiesRunsNoNPMLs(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	npm := filepath.Join(f.npmDir, "npm")
	calls := filepath.Join(f.npmDir, "calls.log")
	f.write(t, npm, strings.Replace(f.read(npm), "#!/bin/sh\n", fmt.Sprintf("#!/bin/sh\necho \"$1\" >>%q\n", calls), 1))
	e := f.env
	e.Repin = true
	if rc := NPM(e); rc != ExitOK {
		t.Fatalf("rc %d\n%s", rc, f.out)
	}
	if got := f.read(calls); strings.Contains(got, "ls") || !strings.Contains(got, "install") {
		t.Fatalf("npm calls %q: want an install and no ls", got)
	}
	// With a local copy the versions are still read (the repin needs them).
	os.WriteFile(calls, nil, 0o644)
	if rc := NPM(e); rc != ExitOK {
		t.Fatalf("rc %d\n%s", rc, f.out)
	}
	if got := f.read(calls); !strings.Contains(got, "ls") {
		t.Fatalf("npm calls %q: the repin no longer reads the versions", got)
	}
}

// cursorKillHome turns the test binary into a cursor self-update (cursorKillHelper) that the
// published candidate stops with SIGKILL at its first --version, right after the publish rename.
const (
	cursorKillHome = "AF_TEST_CURSOR_KILL_HOME"
	cursorKillURL  = "AF_TEST_CURSOR_KILL_URL"
)

func TestCursorKillHelperProcess(t *testing.T) {
	if os.Getenv(cursorKillHome) == "" {
		t.Skip("helper")
	}
	e := Env{Home: os.Getenv(cursorKillHome), Client: http.DefaultClient, Stdout: os.Stderr, Stderr: os.Stderr, RunTimeout: 10 * time.Second}
	err := e.installCursorLatest(context.Background(), os.Getenv(cursorKillURL))
	fmt.Fprintln(os.Stderr, "the update was not stopped:", err)
	os.Exit(4)
}

// A real update stopped right after it published its tree leaves a marker whose token is the
// published tree's own, and recovery takes that tree back out and leaves the launcher as it was.
func TestCursorUpdateStoppedRightAfterPublishingMatchesTheMarkerToken(t *testing.T) {
	if os.Getenv(cursorKillHome) != "" {
		t.Skip("helper")
	}
	// In the stage the candidate answers; at its final path it kills the update that probes it.
	body := `#!/bin/sh\ncase "$0" in */.af-up-*) ;; */versions/2026.2.0-abc123/cursor-agent) kill -9 $PPID; exit 1;; esac\necho 2026.2.0-abc123\n`
	f := newSU(t, goodRTK, "", "", cursorHeader+cursorBundle(body))
	f.plantCursor(t, "1.0.0")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCursorKillHelperProcess$")
	cmd.Env = append(os.Environ(), cursorKillHome+"="+f.home, cursorKillURL+"="+f.env.CursorInstallURL)
	var se bytes.Buffer
	cmd.Stderr = &se
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("the update was not stopped by the candidate: %v\n%s", err, se.String())
	}
	dir := filepath.Join(f.versions(), "2026.2.0-abc123")
	var m cursorMarker
	if b, err := os.ReadFile(f.env.cursorMarkerPath()); err != nil || json.Unmarshal(b, &m) != nil {
		t.Fatalf("no marker after the stop: %v", err)
	}
	tok := f.read(filepath.Join(dir, cursorIDFile))
	if m.ID == "" || tok != m.ID || m.Dir != dir || !m.Tree {
		t.Fatalf("marker %+v does not name the published tree (token %q)", m, tok)
	}
	if owner, err := m.dirOwner(dir); owner != "ours" || err != nil {
		t.Fatalf("recovery would not own the tree it published: %s %v", owner, err)
	}
	if err := f.env.cursorRecover(); err != nil {
		t.Fatal(err)
	}
	if exists(dir) {
		t.Fatal("the unverified published tree stayed")
	}
	if out, _, err := f.env.probeArgs(f.local("cursor-agent"), nil, nil); err != nil || out != "1.0.0" {
		t.Fatalf("launcher: %q %v", out, err)
	}
	if exists(f.env.cursorMarkerPath()) {
		t.Fatal("the marker stayed after recovery")
	}
}
