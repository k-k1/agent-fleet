package cloudexec

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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

// A command that dies of a signal makes the wrapper die of the same one.
func TestSuperviseReRaisesTheCommandsSignal(t *testing.T) {
	cmd := startHelper(t, `kill -HUP $$`)
	err := waitBounded(t, cmd)
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("wrapper ended with %v", err)
	}
	if ws := ee.Sys().(syscall.WaitStatus); !ws.Signaled() || ws.Signal() != syscall.SIGHUP {
		t.Fatalf("wrapper status %v; want death by SIGHUP", ws)
	}
}
