package agents

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// procState returns the one-letter state from /proc/<pid>/stat ("Z" = zombie),
// or "" once the pid is reaped (the /proc entry is gone).
func procState(t *testing.T, pid int) string {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	// Field 3 (state) follows the parenthesized comm, which may contain spaces.
	s := string(b)
	if i := strings.LastIndexByte(s, ')'); i >= 0 && i+2 < len(s) {
		return string(s[i+2])
	}
	return ""
}

// Close must reap the child (Cmd.Wait), not just kill it: workspace-agent is
// not PID 1, so an unwaited flow child stays a zombie until the agent exits
// (on a real machine, `[agy] <defunct>` piled up with every agy /usage scrape — docs/log/32).
func TestCloseReapsKilledProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	f, err := StartFlow(cmd)
	if err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	f.Close()
	if f.Cmd.ProcessState == nil {
		t.Fatal("Close did not reap the child: Cmd.ProcessState is nil (Wait not called)")
	}
	if st := procState(t, pid); st == "Z" {
		t.Fatalf("pid %d is still a zombie after Close", pid)
	}
}

// StartPipeFlow's whole reason to exist is that the child must NOT see a terminal — muse's
// login branches on isatty and stops at a "press Enter" prompt when it has one, instead of
// polling for the device approval (flow.go's StartPipeFlow header).
//
// So the assertion is paired with its control: the same probe under StartFlow must report a
// TTY. Without that arm, a StartPipeFlow that silently fell back to a PTY, or a probe whose
// test was simply inverted, would pass.
func TestPipeFlowGivesTheChildNoTerminal(t *testing.T) {
	const probe = `test -t 0 && echo IN=TTY || echo IN=NOTTY; test -t 1 && echo OUT=TTY || echo OUT=NOTTY`
	for _, tc := range []struct {
		name  string
		start func(*exec.Cmd) (*Flow, error)
		want  string
	}{
		{"pipe", StartPipeFlow, "IN=NOTTY\nOUT=NOTTY"},
		{"pty (control)", StartFlow, "IN=TTY\nOUT=TTY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := tc.start(exec.Command("sh", "-c", probe))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if got := f.WaitFor(regexp.MustCompile(`OUT=\w+`), 5*time.Second); got == "" {
				t.Fatalf("probe printed nothing: %q", f.Clean())
			}
			got := strings.Join(strings.Fields(f.Clean()), "\n")
			if got != tc.want {
				t.Fatalf("child saw %q, want %q", got, tc.want)
			}
		})
	}
}

// Ended is what lets a poll loop stop when the login child has given up (an expired device
// code) rather than spin to its deadline. It must be false while the child lives — otherwise
// a poll would abandon every login immediately, and the test would still pass on the
// "eventually true" half alone.
func TestPipeFlowEndedFollowsTheChild(t *testing.T) {
	f, err := StartPipeFlow(exec.Command("sh", "-c", "echo up; sleep 30"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.WaitFor(regexp.MustCompile("up"), 5*time.Second) == "" {
		t.Fatal("child produced no output")
	}
	if f.Ended() {
		t.Fatal("Ended() is true while the child is still running")
	}

	short, err := StartPipeFlow(exec.Command("sh", "-c", "echo bye"))
	if err != nil {
		t.Fatal(err)
	}
	defer short.Close()
	deadline := time.Now().Add(5 * time.Second)
	for !short.Ended() {
		if time.Now().After(deadline) {
			t.Fatalf("Ended() never became true after the child exited (output %q)", short.Clean())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(short.Clean(), "bye") {
		t.Fatalf("output lost at EOF: %q", short.Clean())
	}
}

// Close must reap a pipe flow's child too, and must not panic on the nil Ptmx a pipe flow
// carries — the PTY arm of this pairing is TestCloseReapsKilledProcess above.
func TestClosePipeFlowReapsKilledProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	f, err := StartPipeFlow(cmd)
	if err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	f.Close()
	if f.Cmd.ProcessState == nil {
		t.Fatal("Close did not reap the child: Cmd.ProcessState is nil (Wait not called)")
	}
	if st := procState(t, pid); st == "Z" {
		t.Fatalf("pid %d is still a zombie after Close", pid)
	}
}

// A flow child that exits on its own before Close (e.g. the CLI crashes at
// startup) sits as a zombie until Close — which must still reap it.
func TestCloseReapsAlreadyExitedProcess(t *testing.T) {
	cmd := exec.Command("true")
	f, err := StartFlow(cmd)
	if err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	// Wait for the child to exit and become a zombie (nobody has waited yet).
	deadline := time.Now().Add(5 * time.Second)
	for procState(t, pid) != "Z" {
		if time.Now().After(deadline) {
			t.Fatalf("pid %d did not become a zombie (state=%q)", pid, procState(t, pid))
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.Close()
	if f.Cmd.ProcessState == nil {
		t.Fatal("Close did not reap the child: Cmd.ProcessState is nil (Wait not called)")
	}
	if st := procState(t, pid); st == "Z" {
		t.Fatalf("pid %d is still a zombie after Close", pid)
	}
}
