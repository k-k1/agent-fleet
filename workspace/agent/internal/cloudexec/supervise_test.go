package cloudexec

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A test binary that re-executes itself as the supervisor, so a signal sent to "the
// wrapper" is a signal to a real process and not to the test run.
func init() {
	if os.Getenv("AF_SUPERVISE_HELPER") != "1" {
		return
	}
	args := os.Args[1:]
	out, err := Supervise(Supervision{Prog: args[0], Argv: args, Env: os.Environ()})
	if err != nil {
		os.Stderr.WriteString("helper: " + err.Error() + "\n")
		os.Exit(99)
	}
	out.Exit()
}

// A session leader on a pseudo-terminal that runs the supervisor helper in a group of its
// own, and moves the terminal's foreground group between the two on SIGUSR1 (to the
// helper's) and SIGUSR2 (to its own), as a shell's fg and bg do.
func init() {
	if os.Getenv("AF_PTY_LEADER") != "1" {
		return
	}
	signal.Ignore(syscall.SIGTTOU)
	self, _ := os.Executable()
	cmd := exec.Command(self, os.Args[1:]...)
	cmd.Env = append(os.Environ(), "AF_SUPERVISE_HELPER=1", "AF_PTY_LEADER=")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGUSR1, syscall.SIGUSR2)
	if err := cmd.Start(); err != nil {
		os.Exit(98)
	}
	pidFile := os.Getenv("AF_PTY_PIDFILE")
	_ = os.WriteFile(pidFile+".tmp", []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
	_ = os.Rename(pidFile+".tmp", pidFile)
	go func() {
		for sig := range sigs {
			pg := syscall.Getpgrp()
			name := "bg"
			if sig == syscall.SIGUSR1 {
				pg, name = cmd.Process.Pid, "fg"
			}
			_ = unix.IoctlSetPointerInt(0, unix.TIOCSPGRP, pg)
			_ = os.WriteFile(pidFile+"."+name, nil, 0o600)
		}
	}()
	_ = cmd.Wait()
	os.Exit(0)
}

func openPTY(t *testing.T) *os.File {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no /dev/ptmx: %v", err)
	}
	t.Cleanup(func() { master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slave.Close() })
	return slave
}

// A group SIGINT (what Ctrl-C is) reaches the command once whichever way the job was moved
// with fg and bg, and a SIGINT sent to the wrapper alone is forwarded once. The foreground
// group is read when the signal arrives, not when the wrapper started.
func TestSuperviseInterruptIsDeliveredOnceAfterFgAndBg(t *testing.T) {
	dir := t.TempDir()
	hits, pidFile := filepath.Join(dir, "hits"), filepath.Join(dir, "pid")
	ready := filepath.Join(dir, "ready")
	self, _ := os.Executable()
	cmd := exec.Command(self, "/bin/sh", "-c", `trap 'echo x >> `+hits+`' INT; touch `+ready+`; while :; do :; done`)
	cmd.Env = append(os.Environ(), "AF_PTY_LEADER=1", "AF_PTY_PIDFILE="+pidFile)
	tty := openPTY(t)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	waitFor(t, "the command to be ready", func() bool { return exists(ready) })
	b, _ := os.ReadFile(pidFile)
	wrapper, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	t.Cleanup(func() { _ = syscall.Kill(-wrapper, syscall.SIGKILL) })

	count := func() int {
		time.Sleep(400 * time.Millisecond) // long enough for a second delivery to show
		b, _ := os.ReadFile(hits)
		return strings.Count(string(b), "x")
	}
	// bg -> fg, then the terminal's Ctrl-C: the whole group gets one SIGINT.
	_ = cmd.Process.Signal(syscall.SIGUSR1)
	waitFor(t, "the group to be in front", func() bool { return exists(pidFile + ".fg") })
	_ = syscall.Kill(-wrapper, syscall.SIGINT)
	if n := count(); n != 1 {
		t.Fatalf("after fg, one group SIGINT reached the command %d times", n)
	}
	// fg -> bg: the terminal no longer delivers, so a SIGINT to the wrapper must be forwarded.
	_ = cmd.Process.Signal(syscall.SIGUSR2)
	waitFor(t, "the group to be behind", func() bool { return exists(pidFile + ".bg") })
	_ = syscall.Kill(wrapper, syscall.SIGINT)
	if n := count(); n != 2 {
		t.Fatalf("after bg, a SIGINT to the wrapper reached the command %d times in total, want 2", n)
	}
}

func sh(script string) (string, []string) {
	return "/bin/sh", []string{"sh", "-c", script}
}

func TestSuperviseExitCodeAndSignal(t *testing.T) {
	prog, argv := sh("exit 7")
	out, err := Supervise(Supervision{Prog: prog, Argv: argv, Env: os.Environ()})
	if err != nil || out.Code != 7 || out.Signal != 0 {
		t.Fatalf("outcome %+v, %v; want exit 7", out, err)
	}
	prog, argv = sh("kill -TERM $$")
	out, err = Supervise(Supervision{Prog: prog, Argv: argv, Env: os.Environ()})
	if err != nil || out.Signal != syscall.SIGTERM || out.Code != 143 {
		t.Fatalf("outcome %+v, %v; want SIGTERM / 143", out, err)
	}
	if _, err := Supervise(Supervision{Prog: "/nonexistent/prog", Argv: []string{"prog"}, Env: nil}); err == nil {
		t.Fatal("a command that cannot start was not an error")
	}
}

func TestSuperviseGivesTheCommandItsEnvironmentOnly(t *testing.T) {
	prog, argv := sh(`[ "$ONLY_HERE" = y ] && [ -z "$HOME_OF_WRAPPER" ]`)
	t.Setenv("HOME_OF_WRAPPER", "set-in-the-wrapper")
	out, err := Supervise(Supervision{Prog: prog, Argv: argv, Env: []string{"ONLY_HERE=y"}})
	if err != nil || out.Code != 0 {
		t.Fatalf("outcome %+v, %v", out, err)
	}
}

// The side runs while the command lives and its context ends with the command; Supervise
// returns only after the side has.
func TestSuperviseSideLivesAndEndsWithTheCommand(t *testing.T) {
	prog, argv := sh("sleep 0.2")
	started, ended := make(chan struct{}), make(chan struct{})
	out, err := Supervise(Supervision{Prog: prog, Argv: argv, Env: os.Environ(),
		Side: func(ctx context.Context, stop func(Stop)) {
			close(started)
			<-ctx.Done()
			time.Sleep(50 * time.Millisecond)
			close(ended)
		}})
	if err != nil || out.Code != 0 {
		t.Fatalf("outcome %+v, %v", out, err)
	}
	select {
	case <-started:
	default:
		t.Fatal("the side never started")
	}
	select {
	case <-ended:
	default:
		t.Fatal("Supervise returned before the side did")
	}
}

func TestSuperviseStopEndsTheCommandWithTheCodeAndMessage(t *testing.T) {
	prog, argv := sh("sleep 30")
	var stderr bytes.Buffer
	start := time.Now()
	out, err := Supervise(Supervision{Prog: prog, Argv: argv, Env: os.Environ(), Stderr: &stderr, Grace: 5 * time.Second,
		Side: func(ctx context.Context, stop func(Stop)) {
			stop(Stop{Code: 3, Msg: "af-test: token ended"})
			stop(Stop{Code: 1, Msg: "ignored second request"})
			<-ctx.Done()
		}})
	if err != nil || out.Code != 3 {
		t.Fatalf("outcome %+v, %v; want exit 3", out, err)
	}
	if got := strings.TrimSpace(stderr.String()); got != "af-test: token ended" {
		t.Fatalf("stderr = %q", got)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("SIGTERM did not end the command")
	}
}

func TestSuperviseStopKillsACommandThatIgnoresSIGTERM(t *testing.T) {
	prog, argv := sh(`trap '' TERM; while :; do sleep 0.05; done`)
	start := time.Now()
	out, err := Supervise(Supervision{Prog: prog, Argv: argv, Env: os.Environ(), Grace: 300 * time.Millisecond,
		Side: func(ctx context.Context, stop func(Stop)) {
			time.Sleep(100 * time.Millisecond) // let the shell install its trap
			stop(Stop{Code: 1})
			<-ctx.Done()
		}})
	if err != nil || out.Code != 1 {
		t.Fatalf("outcome %+v, %v", out, err)
	}
	if d := time.Since(start); d < 300*time.Millisecond || d > 5*time.Second {
		t.Fatalf("ended after %v; expected the grace to pass and then SIGKILL", d)
	}
}

func startHelper(t *testing.T, script string) *exec.Cmd {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(), "AF_SUPERVISE_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

// waitBounded waits for the helper, killing it if it outlasts a few seconds, so a regression
// reads as a failure and not as a hung run.
func waitBounded(t *testing.T, cmd *exec.Cmd) error {
	t.Helper()
	timer := time.AfterFunc(5*time.Second, func() { _ = cmd.Process.Kill() })
	defer timer.Stop()
	err := cmd.Wait()
	if !timer.Stop() {
		t.Error("the wrapper did not end in time")
	}
	return err
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

// SIGKILL of the wrapper takes the command with it (PR_SET_PDEATHSIG): the command never
// runs on without whatever was beside it.
func TestSuperviseCommandDiesWithTheWrapper(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	cmd := startHelper(t, `echo $$ > `+pidFile+`.tmp && mv `+pidFile+`.tmp `+pidFile+`; exec sleep 60`)
	waitFor(t, "the command to start", func() bool { return exists(pidFile) })
	b, _ := os.ReadFile(pidFile)
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	waitFor(t, "the command to die with its wrapper", func() bool {
		// A zombie still has a /proc entry; its state tells.
		st, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		return err != nil || strings.Contains(string(st), ") Z ")
	})
}

// SIGTERM to the wrapper reaches the command, and the command's own status is the wrapper's.
func TestSuperviseForwardsSIGTERM(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	cmd := startHelper(t, `trap 'exit 9' TERM; touch `+ready+`; while :; do sleep 0.05; done`)
	waitFor(t, "the command to be ready", func() bool { return exists(ready) })
	_ = cmd.Process.Signal(syscall.SIGTERM)
	err := waitBounded(t, cmd)
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 9 {
		t.Fatalf("wrapper ended with %v; want exit 9 from the command's trap", err)
	}
}

// A command that dies of a signal makes the wrapper die of the same one, whatever Go would
// have done with that signal by itself (SIGQUIT and SIGABRT: a goroutine dump and exit 2;
// SIGPIPE and SIGUSR1: an ordinary exit).
func TestSuperviseReRaisesTheCommandsSignal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGABRT, syscall.SIGPIPE, syscall.SIGUSR1, syscall.SIGUSR2} {
		t.Run(sig.String(), func(t *testing.T) {
			dir := t.TempDir()
			cmd := startHelper(t, `kill -`+strconv.Itoa(int(sig))+` $$`)
			cmd.Dir = dir // a core file, if the kernel writes one, lands here
			err := waitBounded(t, cmd)
			ee, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("wrapper ended with %v", err)
			}
			if ws := ee.Sys().(syscall.WaitStatus); !ws.Signaled() || ws.Signal() != sig {
				t.Fatalf("wrapper status %v (exit %d); want death by %v", ws, ws.ExitStatus(), sig)
			}
		})
	}
}

func procState(pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	return string(b[strings.LastIndexByte(string(b), ')')+2:][:1])
}

// A command that stops itself stops the wrapper, so the shell sees the job stopped; SIGCONT
// to the wrapper continues the command.
func TestSuperviseStopsWithTheCommandAndContinuesIt(t *testing.T) {
	cmd := startHelper(t, `kill -STOP $$; exit 5`)
	waitFor(t, "the wrapper to stop with its command", func() bool { return procState(cmd.Process.Pid) == "T" })
	_ = cmd.Process.Signal(syscall.SIGCONT)
	err := waitBounded(t, cmd)
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 5 {
		t.Fatalf("wrapper ended with %v; want exit 5 from the continued command", err)
	}
}
