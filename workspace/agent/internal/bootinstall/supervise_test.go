package bootinstall

import (
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

// npmCallerEnv names the env vars that turn the test binary into a caller of npmInstall
// (npmCallerHelper), so a test can kill the caller the way the kernel would kill the Agent.
const (
	npmCallerHome = "AF_TEST_NPM_CALLER_HOME"
	npmCallerNPM  = "AF_TEST_NPM_CALLER_NPM"
)

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

// stragglerNPM writes a stand-in npm that drops its copy of the lock (so only the supervisor
// holds it), starts a straggler that outlives npm unless something kills it, and marks itself
// ready. tail is the rest of its script. It returns the files the script writes.
func stragglerNPM(t *testing.T, f *suFixture, straggler, tail string) (ready, pidf string) {
	t.Helper()
	ready, pidf = filepath.Join(f.home, "ready"), filepath.Join(f.home, "straggler.pid")
	f.write(t, filepath.Join(f.npmDir, "npm"), fmt.Sprintf(
		"#!/bin/sh\nexec 3>&-\n%s </dev/null >/dev/null 2>&1 &\necho $! >%q\n: >%q\n%s", straggler, pidf, ready, tail))
	t.Cleanup(func() { // never leave a sleep behind when a test fails
		if pid, _ := strconv.Atoi(strings.TrimSpace(f.read(pidf))); pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return ready, pidf
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
	}
}

// alive reads the pid from file pidf and reports whether that process still runs (a zombie does
// not: it can write nothing).
func alive(f *suFixture, pidf string) bool {
	pid, _ := strconv.Atoi(strings.TrimSpace(f.read(pidf)))
	if pid <= 0 {
		return false
	}
	p, ok := readStat(pid)
	return ok && p.live
}

func lockBusy(f *suFixture) bool {
	_, _, busy := probeLock(f.env.npmLockPath())
	return busy
}

// A postinstall child that left npm's group with setsid is still npm's descendant: the
// supervisor, as a subreaper, inherits it when npm exits and kills it before the lock goes.
func TestNPMSupervisorKillsADescendantThatLeftTheGroup(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	if _, err := os.Stat("/usr/bin/setsid"); err != nil {
		t.Skip("no setsid")
	}
	_, pidf := stragglerNPM(t, f, "/usr/bin/setsid /bin/sleep 30", "exit 0\n")
	e, rel, ok := f.env.lockNPM()
	if !ok {
		t.Fatal("lock not taken")
	}
	defer rel()
	if se, err := e.npmInstall([]string{"x"}); err != nil {
		t.Fatalf("%v: %s", err, se)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(f.read(pidf)))
	if p, ok := readStat(pid); ok && p.pgrp == pid && p.live {
		t.Fatal("the straggler that left npm's group outlived the install")
	} else if ok && p.live {
		t.Fatalf("the straggler did not leave the group (pgrp %d), so this test proves nothing", p.pgrp)
	}
}

// The supervisor alone is killed (an OOM kill picks one process): the caller still takes npm's
// whole group down before it returns, and the lock is free once it has.
func TestNPMSupervisorKilledAloneTakesTheGroupDown(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	npmPid := filepath.Join(f.home, "npm.pid")
	ready, pidf := stragglerNPM(t, f, "/bin/sleep 30", fmt.Sprintf("echo $$ >%q\nexec /bin/sleep 30\n", npmPid))
	e, rel, ok := f.env.lockNPM()
	if !ok {
		t.Fatal("lock not taken")
	}
	sup := make(chan int, 1)
	e.afterNPMStart = func(pid int) { sup <- pid }
	done := make(chan error, 1)
	go func() { _, err := e.npmInstall([]string{"x"}); done <- err }()
	waitFor(t, "npm never started", func() bool { return exists(ready) && exists(npmPid) && f.read(npmPid) != "" })
	if err := syscall.Kill(<-sup, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a killed supervisor was reported as a successful install")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("npmInstall did not return after its supervisor was killed")
	}
	rel()
	if alive(f, npmPid) || alive(f, pidf) {
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
	ready, pidf := stragglerNPM(t, f, "/bin/sleep 30", fmt.Sprintf(
		"i=0; while [ ! -e %q ] && [ $i -lt 600 ]; do i=$((i+1)); /bin/sleep 0.05; done\n", release))
	caller := exec.Command(os.Args[0], "-test.run=^$")
	caller.Env = append(os.Environ(), npmCallerHome+"="+f.home, npmCallerNPM+"="+f.env.NPM)
	if err := caller.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = caller.Process.Kill(); _ = os.WriteFile(release, nil, 0o644) })
	waitFor(t, "npm never started", func() bool { return exists(ready) })
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
	if alive(f, pidf) {
		t.Fatal("npm's straggler outlived the supervisor")
	}
}

// npm runs past the install's deadline: the group is killed, the install fails, and nothing of
// it is left holding the lock.
func TestNPMInstallDeadlineKillsTheGroup(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	npmPid := filepath.Join(f.home, "npm.pid")
	_, pidf := stragglerNPM(t, f, "/bin/sleep 30", fmt.Sprintf("echo $$ >%q\nexec /bin/sleep 30\n", npmPid))
	e, rel, ok := f.env.lockNPM()
	if !ok {
		t.Fatal("lock not taken")
	}
	e.npmTimeout = 500 * time.Millisecond
	start := time.Now()
	if _, err := e.npmInstall([]string{"x"}); err == nil {
		t.Fatal("an install cut off by its deadline was reported as a success")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("the deadline was not kept: %v", d)
	}
	rel()
	if alive(f, npmPid) || alive(f, pidf) {
		t.Fatal("npm or its straggler outlived the deadline")
	}
	if lockBusy(f) {
		t.Fatal("the lock stayed after the deadline")
	}
}

// A process that took a pid since it was listed is never signalled: the start time no longer
// matches.
func TestKillProcSparesAReusedPid(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	p, ok := readStat(cmd.Process.Pid)
	if !ok {
		t.Fatal("no stat")
	}
	stale := p
	stale.start = "1" // the process that had this pid when it was listed
	killProc(stale)
	time.Sleep(50 * time.Millisecond)
	if q, ok := readStat(p.pid); !ok || !q.live {
		t.Fatal("a process that only shares the listed pid was killed")
	}
	killProc(p)
	waitFor(t, "the listed process was not killed", func() bool { q, ok := readStat(p.pid); return !ok || !q.live })
}
