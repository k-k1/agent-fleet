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
	"strconv"
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
	if b := f.read(f.env.npmStuckPath()); strings.Count(b, "\n") != 1 || !strings.HasPrefix(b, procScope()+" ") {
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
	f.write(t, f.env.npmStuckPath(), fmt.Sprintf("another-boot/pid:[1] %d %d\njunk\n%s x 1\n", p.pid, p.start, procScope()))
	if err := f.env.npmStuckCheck(); err != nil {
		t.Fatalf("an entry of another boot or junk refused the lock: %v", err)
	}
	if exists(f.env.npmStuckPath()) {
		t.Fatal("a record naming nothing that runs stayed")
	}
	// An entry whose scope could not be read when it was written still counts.
	f.write(t, f.env.npmStuckPath(), fmt.Sprintf("? %d %d\n", p.pid, p.start))
	if err := f.env.npmStuckCheck(); !errors.Is(err, errNPMStuck) {
		t.Fatalf("an entry of an unknown scope was dropped: %v", err)
	}
	os.Remove(f.env.npmStuckPath())
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

// The every-start pass whose first lock attempt fails and whose retry gets the lock settles the
// accepted update's backups in the same start, under that one hold.
func TestNPMVerifyPresentSettlesBackupsWhenItsRetryGetsTheLock(t *testing.T) {
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
		if tries++; tries == 2 {
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

// With no usable lock at either attempt, the every-start pass takes it nowhere else (another
// start could have left unverified backups by then) and deletes nothing.
func TestNPMVerifyPresentWithoutTheLockTakesItNowhereElse(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "2.0.0")
	f.write(t, tree+npmBakSuffix+"/VERSION", "1.0.0")
	f.write(t, f.env.npmMarker(), "accepted\nclaude\n")
	if err := os.MkdirAll(f.env.npmLockPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	tries := 0
	e := f.env
	e.beforeNPMLock = func() {
		if tries++; tries == 3 {
			os.Remove(e.npmLockPath()) // a later attempt would succeed: none may be made
		}
	}
	if rc := e.NPMVerifyPresentStep(); rc != ExitOK {
		t.Fatalf("rc %d\n%s", rc, f.out)
	}
	if tries != 2 {
		t.Fatalf("%d lock attempts, want the step's own two", tries)
	}
	if !exists(tree + npmBakSuffix) {
		t.Fatal("a backup was deleted without the lock")
	}
}

// stuckNPM writes an npm that, from its install number n on, installs as the shared stub does,
// then leaves a writer that left its group (setsid) and exits 0; earlier installs and every other
// command go to the stub alone. With
// superviseNoKillEnv the supervisor cannot stop that writer, so it reports it.
func stuckNPM(t *testing.T, f *suFixture, n int) (pidf string) {
	t.Helper()
	pidf = filepath.Join(f.home, "writer.pid")
	token := fmt.Sprintf("30.%09d", time.Now().Nanosecond())
	stub := filepath.Join(f.npmDir, "npm-stub")
	if err := os.Rename(filepath.Join(f.npmDir, "npm"), stub); err != nil {
		t.Fatal(err)
	}
	count := filepath.Join(f.npmDir, "installs")
	f.write(t, filepath.Join(f.npmDir, "npm"), fmt.Sprintf(`#!/bin/sh
exec 3>&-
[ "$1" = install ] || exec %[1]q "$@"
echo x >>%[2]q
[ "$(wc -l <%[2]q)" -ge %[3]d ] || exec %[1]q "$@"
%[1]q "$@" || exit 1
/usr/bin/setsid /bin/sh -c 'echo $$ >%[4]q; exec /bin/sleep %[5]s' </dev/null >/dev/null 2>&1 &
i=0; while [ ! -s %[4]q ] && [ $i -lt 600 ]; do i=$((i+1)); /bin/sleep 0.05; done
exit 0
`, stub, count, n, pidf, token))
	// The stub finds its files beside itself ($D): keep them there.
	t.Setenv(superviseNoKillEnv, "1")
	t.Setenv(superviseGraceEnv, "200ms")
	n2 := fakeNPM{token: token}
	t.Cleanup(func() { n2.kill(f, pidf) })
	return pidf
}

func needSetsid(t *testing.T) {
	if _, err := os.Stat("/usr/bin/setsid"); err != nil {
		t.Skip("no setsid")
	}
}

// The supervisor reports a child it could not stop on its stdout, and the real install records it
// from there: the writer left npm's group, so the caller's own listing never sees it.
func TestNPMInstallRecordsWhatTheSupervisorReports(t *testing.T) {
	needSetsid(t)
	f := newSU(t, goodRTK, "", "", "")
	pidf := stuckNPM(t, f, 1)
	e, rel := lockedEnv(t, f)
	_, err := e.npmInstall([]string{"x"})
	rel()
	if !errors.Is(err, errNPMStuck) {
		t.Fatalf("got %v, want errNPMStuck", err)
	}
	pid := strings.TrimSpace(f.read(pidf))
	if pid == "" || !strings.Contains(f.read(f.env.npmStuckPath()), " "+pid+" ") {
		t.Fatalf("the reported writer %q is not recorded: %q", pid, f.read(f.env.npmStuckPath()))
	}
	if _, _, ok := f.env.lockNPM(); ok {
		t.Fatal("the lock was granted while the reported writer runs")
	}
	fakeNPM{token: tokenOf(t, f, pidf)}.kill(f, pidf)
	waitFor(t, "the lock stayed refused after the writer was gone", func() bool {
		_, release, ok := f.env.lockNPM()
		release()
		return ok
	})
}

// tokenOf reads back the sleep token of the process pidf names.
func tokenOf(t *testing.T, f *suFixture, pidf string) string {
	t.Helper()
	b, _ := os.ReadFile("/proc/" + strings.TrimSpace(f.read(pidf)) + "/cmdline")
	parts := strings.Split(string(b), "\x00")
	for _, p := range parts {
		if strings.HasPrefix(p, "30.") {
			return p
		}
	}
	t.Fatalf("no token in %q", b)
	return ""
}

// A pin repair whose npm placed a link and left a writer does not remove that link.
func TestNPMVerifyRepairWithAStuckWriterRemovesNothing(t *testing.T) {
	needSetsid(t)
	f := newSU(t, goodRTK, "", "", "")
	stuckNPM(t, f, 1)
	os.MkdirAll(filepath.Dir(f.local("claude")), 0o755)
	f.write(t, f.local("claude"), "#!/bin/sh\nexit 1\n") // does not run: the verify repairs it
	if rc := f.env.NPMVerify([]string{"claude"}, false); rc != ExitFailed {
		t.Fatalf("rc %d\n%s", rc, f.out)
	}
	if !usable(f.local("claude")) {
		t.Fatalf("the repair's link was removed under a writer it could not stop\n%s", f.out)
	}
	if !strings.Contains(f.out.String(), "did not stop") {
		t.Fatalf("no warning:\n%s", f.out)
	}
}

// A self-update whose new copies run but whose pin repair of a CLI without a previous copy leaves
// a writer settles nothing: the marker stays pending and the backups stay for the recovery.
func TestSelfUpdateNPMRepairWithAStuckWriterSettlesNothing(t *testing.T) {
	needSetsid(t)
	f := newSU(t, goodRTK, "", "", "")
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "1.0.0")
	f.write(t, filepath.Join(f.npmDir, "body.codex"), "#!/bin/sh\nexit 1\n") // the new codex does not run
	stuckNPM(t, f, 2)                                                        // the update installs; the repair leaves a writer
	f.env.selfUpdateNPM()
	if state, _, ok, _ := f.env.npmReadMarker(); !ok || state != "pending" {
		t.Fatalf("marker %q %v, want it left pending\n%s", state, ok, f.out)
	}
	if !exists(tree + npmBakSuffix) {
		t.Fatalf("the previous copy was deleted under a running writer\n%s", f.out)
	}
	if !strings.Contains(f.out.String(), "not settled") {
		t.Fatalf("no warning:\n%s", f.out)
	}
}

// A process whose leader thread exited while another thread runs is still in its group: the
// caller's listing (the path without the supervisor's report) counts and records it.
func TestGroupListingCountsAProcessWhoseLeaderThreadExited(t *testing.T) {
	py, err := exec.LookPath("/usr/bin/python3")
	if err != nil {
		t.Skip("no python3")
	}
	f := newSU(t, goodRTK, "", "", "")
	sp := filepath.Join(f.home, "leader.py")
	f.write(t, sp, `import ctypes, threading, time
threading.Thread(target=lambda: time.sleep(20)).start()
ctypes.CDLL(None).pthread_exit(None)
`)
	cmd := exec.Command(py, sp)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	pid := cmd.Process.Pid
	waitFor(t, "the leader thread never exited", func() bool { st, ok := readStat(pid); return ok && !st.live })
	got := groupProcs(pid)
	if len(got) != 1 || got[0].pid != pid || !procRuns(got[0]) {
		t.Fatalf("group listing %v, want the process whose leader exited", got)
	}
	if waitGroupGone(pid, 100*time.Millisecond) {
		t.Fatal("the group counted as empty while a thread still runs")
	}
	if runs, err := f.env.npmRecordStuck(got); !runs || err != nil {
		t.Fatalf("not recorded: %v %v", runs, err)
	}
}

// A procfs of another pid namespace records nothing and holds nothing back: its numbers name
// other processes.
func TestNPMStuckIgnoresAForeignProcfs(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	_, p := startSleeper(t)
	if runs, err := f.env.npmRecordStuck([]npmProc{p}); !runs || err != nil {
		t.Fatalf("setup: %v %v", runs, err)
	}
	fake := t.TempDir()
	if err := os.Symlink("999999999", filepath.Join(fake, "self")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("/proc", strconv.Itoa(p.pid)), filepath.Join(fake, strconv.Itoa(p.pid))); err != nil {
		t.Fatal(err)
	}
	old := procRoot
	procRoot = fake
	t.Cleanup(func() { procRoot = old })
	if err := f.env.npmStuckCheck(); err != nil {
		t.Fatalf("a foreign procfs held the lock back: %v", err)
	}
	os.Remove(f.env.npmStuckPath())
	if runs, _ := f.env.npmRecordStuck([]npmProc{p}); runs || exists(f.env.npmStuckPath()) {
		t.Fatal("a process seen through a foreign procfs was recorded")
	}
}

// A FIFO at the record's path neither blocks the lock nor counts as "nothing runs".
func TestNPMStuckRecordThatIsAFIFORefusesWithoutBlocking(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	os.MkdirAll(filepath.Dir(f.env.npmStuckPath()), 0o755)
	if err := syscall.Mkfifo(f.env.npmStuckPath(), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- f.env.npmStuckCheck() }()
	select {
	case err := <-done:
		if !errors.Is(err, errNPMStuck) {
			t.Fatalf("a FIFO record let the lock through: %v", err)
		}
	case <-time.After(5 * time.Second):
		os.WriteFile(f.env.npmStuckPath(), nil, 0o600) // unblock the open before failing
		t.Fatal("the check blocked on a FIFO")
	}
}
