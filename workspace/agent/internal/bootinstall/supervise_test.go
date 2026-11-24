package bootinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// npmCallerHome and npmCallerNPM turn the test binary into a caller of npmInstall
// (npmCallerHelper), so a test can kill the caller the way the kernel would kill the Agent.
const (
	npmCallerHome = "AF_TEST_NPM_CALLER_HOME"
	npmCallerNPM  = "AF_TEST_NPM_CALLER_NPM"
)

// superviseGraceEnv shortens the grace of a supervisor started from the test binary (TestMain),
// so a cleanup that runs out of time can be seen without an unkillable process.
const superviseGraceEnv = "AF_TEST_NPM_SUPERVISE_GRACE"

// npmCallerHelper takes the npm lock and runs one supervised install, as NPM does.
func npmCallerHelper() int {
	e, _, ok := Env{Home: os.Getenv(npmCallerHome), NPM: os.Getenv(npmCallerNPM)}.lockNPM()
	if !ok {
		return 3
	}
	if _, err := e.npmInstall([]string{"x"}); err != nil {
		return 1
	}
	return 0
}

// fakeNPM is a stand-in npm that drops its copy of the lock (so only the supervisor holds it),
// starts a straggler that outlives npm unless something kills it, waits until the straggler has
// written its own pid (after any setsid, so it is in its final group), writes its own pid and a
// ready file, then runs tail. launch and tail name the straggler's files {PIDF} and {SREADY}
// and the sleep duration {TOKEN}. Every process it starts sleeps with token as its duration, and the
// cleanup signals a recorded pid only through a pidfd whose command line still carries the
// token, so a pid reused by someone else on the shared host is never hit.
type fakeNPM struct {
	ready, npmPid, strayPid, token string
}

func newFakeNPM(t *testing.T, f *suFixture, launch, tail string) fakeNPM {
	t.Helper()
	n := fakeNPM{
		ready: filepath.Join(f.home, "ready"), npmPid: filepath.Join(f.home, "npm.pid"),
		strayPid: filepath.Join(f.home, "straggler.pid"),
		token:    fmt.Sprintf("30.%09d", time.Now().Nanosecond()),
	}
	sready := filepath.Join(f.home, "straggler.ready")
	stray := strings.NewReplacer("{PIDF}", strconv.Quote(n.strayPid), "{SREADY}", strconv.Quote(sready), "{TOKEN}", n.token).Replace(launch)
	tail = strings.ReplaceAll(tail, "{TOKEN}", n.token)
	f.write(t, filepath.Join(f.npmDir, "npm"), fmt.Sprintf("#!/bin/sh\nexec 3>&-\n%s </dev/null >/dev/null 2>&1 &\n"+
		"i=0; while [ ! -e %q ] && [ $i -lt 600 ]; do i=$((i+1)); /bin/sleep 0.05; done\n"+
		"echo $$ >%q\n: >%q\n%s", stray, sready, n.npmPid, n.ready, tail))
	t.Cleanup(func() { n.kill(f, n.strayPid); n.kill(f, n.npmPid) })
	return n
}

// shStraggler is a straggler in npm's group; setsidStraggler leaves it first.
const (
	shStraggler     = "/bin/sh -c 'echo $$ >{PIDF}; : >{SREADY}; exec /bin/sleep {TOKEN}'"
	setsidStraggler = "/usr/bin/setsid " + shStraggler
)

func (n fakeNPM) kill(f *suFixture, pidf string) {
	pid, _ := strconv.Atoi(strings.TrimSpace(f.read(pidf)))
	if pid <= 0 {
		return
	}
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return
	}
	defer unix.Close(fd)
	if b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); strings.Contains(string(b), n.token) {
		_ = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
	}
}

// runs reports whether the process whose pid file pidf names still has a running thread. Its
// stat alone is not enough: it says zombie as soon as the leader thread has exited, while the
// other threads may still run. A missing pid file fails the test: the process never started, and
// "not running" would prove nothing.
func (n fakeNPM) runs(t *testing.T, f *suFixture, pidf string) bool {
	t.Helper()
	pid, _ := strconv.Atoi(strings.TrimSpace(f.read(pidf)))
	if pid <= 0 {
		t.Fatalf("%s holds no pid", filepath.Base(pidf))
	}
	tasks, _ := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	for _, d := range tasks {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%s/stat", pid, d.Name()))
		if i := strings.LastIndexByte(string(b), ')'); err == nil && i >= 0 && i+2 < len(b) && b[i+2] != 'Z' && b[i+2] != 'X' {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
	}
}

func lockBusy(f *suFixture) bool {
	_, _, busy := probeLock(f.env.npmLockPath())
	return busy
}

// install is one npmInstall running in the background.
type install struct {
	sup      *os.Process
	finished chan struct{}
	err      error
}

// wait returns the install's result, failing the test when it does not come in time.
func (in *install) wait(t *testing.T) error {
	t.Helper()
	select {
	case <-in.finished:
		return in.err
	case <-time.After(15 * time.Second):
		t.Fatal("npmInstall did not return")
	}
	return nil
}

// startInstall runs npmInstall in the background and returns once its supervisor has started and
// npm is ready. The lock is released only after the install has returned (closing it earlier
// races Cmd.Start reading it), on every path out of the test.
func startInstall(t *testing.T, f *suFixture, e Env, rel func(), n fakeNPM) *install {
	t.Helper()
	in := &install{finished: make(chan struct{})}
	supc := make(chan *os.Process, 1)
	e.afterNPMStart = func(p *os.Process) { supc <- p }
	go func() {
		se, err := e.npmInstall([]string{"x"})
		if err != nil {
			err = fmt.Errorf("%w: %s", err, se)
		}
		in.err = err
		close(in.finished)
	}()
	t.Cleanup(func() {
		n.kill(f, n.strayPid)
		n.kill(f, n.npmPid)
		select {
		case <-in.finished:
			rel()
		case <-time.After(20 * time.Second): // leave the descriptor open rather than race Start
		}
	})
	select {
	case in.sup = <-supc:
	case <-in.finished:
		// The hook runs before the install can return, so a started supervisor is already here
		// even when select picked this case.
		select {
		case in.sup = <-supc:
		default:
			t.Fatalf("npmInstall returned before its supervisor started: %v", in.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the supervisor never started (npm ran directly?)")
	}
	waitFor(t, "npm never got ready", func() bool { return exists(n.ready) })
	return in
}

func lockedEnv(t *testing.T, f *suFixture) (Env, func()) {
	t.Helper()
	e, rel, ok := f.env.lockNPM()
	if !ok {
		t.Fatal("lock not taken")
	}
	return e, rel
}

// A postinstall child that left npm's group with setsid is still npm's descendant: the
// supervisor, as a subreaper, inherits it when npm exits and kills it before the lock goes.
func TestNPMSupervisorKillsADescendantThatLeftTheGroup(t *testing.T) {
	if _, err := os.Stat("/usr/bin/setsid"); err != nil {
		t.Skip("no setsid")
	}
	f := newSU(t, goodRTK, "", "", "")
	n := newFakeNPM(t, f, setsidStraggler, "exit 0\n")
	e, rel := lockedEnv(t, f)
	if err := startInstall(t, f, e, rel, n).wait(t); err != nil {
		t.Fatal(err)
	}
	if n.runs(t, f, n.strayPid) {
		t.Fatal("the straggler that left npm's group outlived the install")
	}
}

// npm leaves a live helper in its own session, which has a live child in yet another session:
// the supervisor kills the helper, inherits the grandchild when the helper dies, and kills that
// too, all before it reports success.
func TestNPMSupervisorKillsAGrandchildItInherits(t *testing.T) {
	if _, err := os.Stat("/usr/bin/setsid"); err != nil {
		t.Skip("no setsid")
	}
	f := newSU(t, goodRTK, "", "", "")
	gpid, helper := filepath.Join(f.home, "grandchild.pid"), filepath.Join(f.home, "helper.sh")
	// {SREADY} is written by the grandchild, so npm exits only once both run.
	n := newFakeNPM(t, f, "/usr/bin/setsid /bin/sh "+helper, "exit 0\n")
	f.write(t, helper, strings.NewReplacer("{PIDF}", strconv.Quote(n.strayPid), "{GPID}", strconv.Quote(gpid),
		"{SREADY}", strconv.Quote(filepath.Join(f.home, "straggler.ready")), "{TOKEN}", n.token).Replace(
		"echo $$ >{PIDF}\n/usr/bin/setsid /bin/sh -c 'echo $$ >{GPID}; : >{SREADY}; exec /bin/sleep {TOKEN}' </dev/null >/dev/null 2>&1 &\nexec /bin/sleep {TOKEN}\n"))
	t.Cleanup(func() { n.kill(f, gpid) })
	e, rel := lockedEnv(t, f)
	if err := startInstall(t, f, e, rel, n).wait(t); err != nil {
		t.Fatal(err)
	}
	if n.runs(t, f, n.strayPid) || n.runs(t, f, gpid) {
		t.Fatal("the helper or its grandchild outlived the install")
	}
}

// A postinstall helper whose main thread exited while its other threads run shows as a zombie
// in its stat; it is still killed, so the install neither waits out the grace nor leaves it
// running. It left the group, so the caller's group kill would not reach it either.
func TestNPMSupervisorKillsAChildWhoseLeaderThreadExited(t *testing.T) {
	py, err := exec.LookPath("/usr/bin/python3")
	if err != nil {
		t.Skip("no python3")
	}
	if _, err := os.Stat("/usr/bin/setsid"); err != nil {
		t.Skip("no setsid")
	}
	f := newSU(t, goodRTK, "", "", "")
	// The thread reports ready only once the leader is a zombie, so npm exits after that.
	script := `import ctypes, os, threading, time
def run():
    while open("/proc/%d/stat" % os.getpid()).read().rsplit(")", 1)[1].split()[0] != "Z":
        time.sleep(0.01)
    open(os.environ["SREADY_FILE"], "w").close()
    time.sleep(10)  # bounded: once the leader is gone its cmdline is empty, so no cleanup can match it
open(os.environ["PID_FILE"], "w").write(str(os.getpid()))
threading.Thread(target=run).start()
ctypes.CDLL(None).pthread_exit(None)
`
	sp := filepath.Join(f.home, "leader.py")
	f.write(t, sp, script)
	launch := fmt.Sprintf("PID_FILE={PIDF} SREADY_FILE={SREADY} /usr/bin/setsid %s %s {TOKEN}", py, sp)
	n := newFakeNPM(t, f, launch, "exit 0\n")
	e, rel := lockedEnv(t, f)
	if err := startInstall(t, f, e, rel, n).wait(t); err != nil {
		t.Fatal(err)
	}
	if n.runs(t, f, n.strayPid) {
		t.Fatal("a child whose leader thread exited outlived the install")
	}
}

// The supervisor alone is killed (an OOM kill picks one process): the caller still takes npm's
// whole group down before it returns, and the lock is free once it has.
func TestNPMSupervisorKilledAloneTakesTheGroupDown(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	n := newFakeNPM(t, f, shStraggler, "exec /bin/sleep {TOKEN}\n")
	e, rel := lockedEnv(t, f)
	in := startInstall(t, f, e, rel, n)
	if err := in.sup.Kill(); err != nil { // through the Process: never a pid reused since
		t.Fatal(err)
	}
	if err := in.wait(t); err == nil {
		t.Fatal("a killed supervisor was reported as a successful install")
	}
	rel()
	if n.runs(t, f, n.npmPid) || n.runs(t, f, n.strayPid) {
		t.Fatal("npm or its straggler outlived the killed supervisor")
	}
	if lockBusy(f) {
		t.Fatal("the lock stayed after the group was gone")
	}
}

// The caller is killed once npm runs: the supervisor keeps the lock while npm and its straggler
// still run, and lets it go only after both are gone.
func TestNPMSupervisorOutlivesAKilledCaller(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	release := filepath.Join(f.home, "release")
	n := newFakeNPM(t, f, shStraggler, fmt.Sprintf(
		"i=0; while [ ! -e %q ] && [ $i -lt 600 ]; do i=$((i+1)); /bin/sleep 0.05; done\n", release))
	caller := exec.Command(os.Args[0], "-test.run=^$")
	caller.Env = append(os.Environ(), npmCallerHome+"="+f.home, npmCallerNPM+"="+f.env.NPM)
	if err := caller.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = caller.Process.Kill()
		_ = caller.Wait()
		_ = os.WriteFile(release, nil, 0o644)
	})
	waitFor(t, "npm never got ready", func() bool { return exists(n.ready) })
	if err := caller.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = caller.Wait()
	if !lockBusy(f) {
		t.Fatal("the lock was free while npm still ran after its caller was killed")
	}
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the lock stayed after npm finished", func() bool { return !lockBusy(f) })
	if n.runs(t, f, n.strayPid) {
		t.Fatal("npm's straggler outlived the supervisor")
	}
}

// The install's deadline fires while npm runs: the group is killed, the install fails, and
// nothing of it is left holding the lock.
func TestNPMInstallDeadlineKillsTheGroup(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	n := newFakeNPM(t, f, shStraggler, "exec /bin/sleep {TOKEN}\n")
	e, rel := lockedEnv(t, f)
	ctx, fire := context.WithCancel(context.Background())
	defer fire()
	e.npmCtx = ctx
	in := startInstall(t, f, e, rel, n)
	fire()
	if err := in.wait(t); err == nil {
		t.Fatal("an install cut off by its deadline was reported as a success")
	}
	rel()
	if n.runs(t, f, n.npmPid) || n.runs(t, f, n.strayPid) {
		t.Fatal("npm or its straggler outlived the deadline")
	}
	if lockBusy(f) {
		t.Fatal("the lock stayed after the deadline")
	}
}

// The supervisor's grace runs out with a child still there: a successful npm becomes a failure
// with the reason on stderr, while a failed npm keeps its own exit code.
func TestNPMSupervisorFailsWhenItsGraceRunsOut(t *testing.T) {
	for _, c := range []struct {
		exit, want int
		reason     bool
	}{{0, 1, true}, {7, 7, false}} {
		f := newSU(t, goodRTK, "", "", "")
		n := newFakeNPM(t, f, shStraggler, fmt.Sprintf("exit %d\n", c.exit))
		t.Setenv(superviseGraceEnv, "0s") // the straggler is alive at the first look, and no second one comes
		e, rel := lockedEnv(t, f)
		err := startInstall(t, f, e, rel, n).wait(t)
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != c.want {
			t.Fatalf("npm exit %d: got %v, want exit %d", c.exit, err, c.want)
		}
		if got := strings.Contains(err.Error(), "did not stop"); got != c.reason {
			t.Fatalf("npm exit %d: reason on stderr %v, want %v: %v", c.exit, got, c.reason, err)
		}
		if n.runs(t, f, n.strayPid) {
			t.Fatal("the straggler outlived the install")
		}
	}
}
