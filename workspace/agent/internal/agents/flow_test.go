package agents

import (
	"fmt"
	"os"
	"os/exec"
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
