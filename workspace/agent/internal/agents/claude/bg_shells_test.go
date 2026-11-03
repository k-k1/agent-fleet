package claude

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/procx"
)

// shellTree is pane root → claude → a background bash (200) → cat (201), the tree #3279
// measured, plus an unrelated bash (300) that is not under claude.
func shellTree() map[int]procInfo {
	return map[int]procInfo{
		100: {PPID: 1, State: 'S', Comm: "bash"},
		101: {PPID: 100, State: 'S', Comm: "claude"},
		200: {PPID: 101, State: 'S', Comm: "bash", Pgrp: 200, StartTicks: 5000, CPUTicks: 2},
		201: {PPID: 200, State: 'S', Comm: "cat", Pgrp: 200, StartTicks: 5001, CPUTicks: 1},
		300: {PPID: 1, State: 'S', Comm: "bash", Pgrp: 300, StartTicks: 10},
	}
}

func TestBackgroundShellsIn(t *testing.T) {
	withArgv(t, map[int][]string{
		200: {"/bin/bash", "-c", `source /x/snap.sh 2>/dev/null || true && eval 'cat > /tmp/x 2>/dev/null; echo '"'"'hi'"'"'' && pwd -P >| /tmp/claude-1-cwd`},
	})
	boot := time.Unix(1_700_000_000, 0)

	t.Run("lists the hung shell with its command, age and CPU", func(t *testing.T) {
		got := backgroundShellsIn(100, shellTree(), boot)
		if len(got) != 1 {
			t.Fatalf("got %d shells, want 1: %+v", len(got), got)
		}
		sh := got[0]
		if sh.PID != 200 || sh.Start != 5000 {
			t.Fatalf("pid/start = %d/%d", sh.PID, sh.Start)
		}
		if sh.Command != `cat > /tmp/x 2>/dev/null; echo 'hi'` {
			t.Fatalf("command = %q", sh.Command)
		}
		if sh.StartedAt != boot.Add(50*time.Second).Format(time.RFC3339) {
			t.Fatalf("startedAt = %q", sh.StartedAt)
		}
		if sh.Running || sh.CPUSec != 0.03 {
			t.Fatalf("running=%v cpu=%v, want false and 0.03", sh.Running, sh.CPUSec)
		}
	})

	t.Run("a shell busy on its own (a builtin loop, no child) is running", func(t *testing.T) {
		tab := shellTree()
		delete(tab, 201)
		sh := tab[200]
		sh.State = 'R'
		tab[200] = sh
		if got := backgroundShellsIn(100, tab, boot); len(got) != 1 || !got[0].Running {
			t.Fatalf("want running, got %+v", got)
		}
	})

	t.Run("a child on a CPU makes it running", func(t *testing.T) {
		tab := shellTree()
		c := tab[201]
		c.State = 'R'
		tab[201] = c
		if got := backgroundShellsIn(100, tab, boot); len(got) != 1 || !got[0].Running {
			t.Fatalf("want running, got %+v", got)
		}
	})

	t.Run("oldest first", func(t *testing.T) {
		tab := shellTree()
		tab[210] = procInfo{PPID: 101, State: 'S', Comm: "bash", StartTicks: 4000}
		got := backgroundShellsIn(100, tab, boot)
		if len(got) != 2 || got[0].PID != 210 || got[1].PID != 200 {
			t.Fatalf("order: %+v", got)
		}
	})

	t.Run("a shell wrapping another claude is not listed", func(t *testing.T) {
		tab := shellTree()
		tab[202] = procInfo{PPID: 200, State: 'S', Comm: "claude"}
		if got := backgroundShellsIn(100, tab, boot); len(got) != 0 {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestShellCommand(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"bash", "-c", `x && eval 'sleep 5' && pwd -P >| /tmp/c`}, "sleep 5"},
		{[]string{"bash", "-c", `eval 'a '"'"'b'"'"''`}, "a 'b'"},
		{[]string{"bash", "-c", "while true; do sleep 30; done"}, "bash -c while true; do sleep 30; done"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := shellCommand(c.argv); got != c.want {
			t.Errorf("shellCommand(%q) = %q, want %q", c.argv, got, c.want)
		}
	}
}

func TestCheckBGShell(t *testing.T) {
	if err := checkBGShell(100, shellTree(), 200, 5000); err != nil {
		t.Fatalf("valid shell refused: %v", err)
	}
	refused := []struct {
		name  string
		pid   int
		start uint64
	}{
		{"start time changed (pid reused)", 200, 4999},
		{"not under this pane", 300, 10},
		{"under the shell but not a background shell itself", 201, 5001},
		{"claude itself", 101, 0},
		{"gone", 999, 0},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			if err := checkBGShell(100, shellTree(), c.pid, c.start); !errors.Is(err, ErrBGShellGone) {
				t.Fatalf("err = %v, want ErrBGShellGone", err)
			}
		})
	}
}

// fakeProcs stands in for /proc and pidfds. A pin is on a process identity (pid + start), so
// a signal through it reaches that process only; one whose process has gone does nothing.
type fakeProcs struct {
	tab        map[int]procInfo
	ignoreTerm map[int]bool
	sent       []string
	// onSignal runs after each delivered signal, to change the table mid-stop.
	onSignal func(pid int, sig syscall.Signal)
	// beforePin runs just before a pin is opened, to race the table against it.
	beforePin func(pid int)
}

type fakePin struct {
	f *fakeProcs
	t bgTarget
}

func (p fakePin) Signal(sig syscall.Signal) error {
	pi, ok := p.f.tab[p.t.pid]
	if !ok || pi.StartTicks != p.t.start {
		return syscall.ESRCH
	}
	p.f.sent = append(p.f.sent, sig.String()+":"+strconv.Itoa(p.t.pid))
	if sig == syscall.SIGKILL || (sig == syscall.SIGTERM && !p.f.ignoreTerm[p.t.pid]) {
		delete(p.f.tab, p.t.pid)
	}
	if p.f.onSignal != nil {
		p.f.onSignal(p.t.pid, sig)
	}
	return nil
}

func (fakePin) Close() {}

func (f *fakeProcs) install(t *testing.T) {
	t.Helper()
	oPin, oFresh, oRoot, oGrace := pinProc, freshProcs, paneRoot, bgStopGrace
	t.Cleanup(func() { pinProc, freshProcs, paneRoot, bgStopGrace = oPin, oFresh, oRoot, oGrace })
	bgStopGrace = 300 * time.Millisecond
	paneRoot = func(string) int { return 100 }
	freshProcs = func() map[int]procInfo {
		cp := map[int]procInfo{}
		for k, v := range f.tab {
			cp[k] = v
		}
		return cp
	}
	pinProc = func(pid int, start uint64) (procPin, bool) {
		if f.beforePin != nil {
			f.beforePin(pid)
		}
		if pi, ok := f.tab[pid]; !ok || pi.StartTicks != start {
			return nil, false
		}
		return fakePin{f, bgTarget{pid, start}}, true
	}
}

func (f *fakeProcs) signalled(s string) bool {
	for _, x := range f.sent {
		if x == s {
			return true
		}
	}
	return false
}

func TestStopBackgroundShell(t *testing.T) {
	t.Run("SIGTERM is enough, children before the shell", func(t *testing.T) {
		f := &fakeProcs{tab: shellTree()}
		f.install(t)
		if err := StopBackgroundShell("s", 200, 5000); err != nil {
			t.Fatal(err)
		}
		want := []string{"terminated:201", "terminated:200"}
		if len(f.sent) != 2 || f.sent[0] != want[0] || f.sent[1] != want[1] {
			t.Fatalf("sent %v, want %v", f.sent, want)
		}
	})

	t.Run("SIGKILL after the grace, only for what is left", func(t *testing.T) {
		f := &fakeProcs{tab: shellTree(), ignoreTerm: map[int]bool{201: true}}
		f.install(t)
		if err := StopBackgroundShell("s", 200, 5000); err != nil {
			t.Fatal(err)
		}
		if _, ok := f.tab[201]; ok {
			t.Fatal("cat survived")
		}
		if last := f.sent[len(f.sent)-1]; last != "killed:201" {
			t.Fatalf("last signal %q, sent %v", last, f.sent)
		}
	})

	t.Run("a pid reused between the scan and the pin is never signalled", func(t *testing.T) {
		f := &fakeProcs{tab: shellTree()}
		f.install(t)
		// 201 exits after the scan and its pid goes to an unrelated process before the pin opens.
		f.beforePin = func(pid int) {
			if pid == 201 {
				f.tab[201] = procInfo{PPID: 1, State: 'S', Comm: "other", StartTicks: 9999}
			}
		}
		if err := StopBackgroundShell("s", 200, 5000); err != nil {
			t.Fatal(err)
		}
		if f.signalled("terminated:201") || f.signalled("killed:201") || f.tab[201].Comm != "other" {
			t.Fatalf("the reused pid was signalled: %v", f.sent)
		}
	})

	t.Run("a pid reused during the grace is never signalled", func(t *testing.T) {
		f := &fakeProcs{tab: shellTree(), ignoreTerm: map[int]bool{200: true, 201: true}}
		f.install(t)
		f.onSignal = func(pid int, sig syscall.Signal) {
			if pid == 201 && sig == syscall.SIGTERM {
				f.tab[201] = procInfo{PPID: 1, State: 'S', Comm: "other", StartTicks: 9999}
			}
		}
		if err := StopBackgroundShell("s", 200, 5000); err != nil {
			t.Fatal(err)
		}
		if f.signalled("killed:201") || f.tab[201].Comm != "other" {
			t.Fatalf("SIGKILL reached the reused pid: %v", f.sent)
		}
	})

	t.Run("a group member that re-parented away and ignores SIGTERM is killed", func(t *testing.T) {
		tab := shellTree()
		tab[205] = procInfo{PPID: 1, State: 'S', Comm: "daemon", Pgrp: 200, StartTicks: 5005}
		f := &fakeProcs{tab: tab, ignoreTerm: map[int]bool{205: true}}
		f.install(t)
		if err := StopBackgroundShell("s", 200, 5000); err != nil {
			t.Fatal(err)
		}
		if _, ok := f.tab[205]; ok || !f.signalled("killed:205") {
			t.Fatalf("group member survived: sent %v", f.sent)
		}
	})

	t.Run("a child forked during the stop is taken in", func(t *testing.T) {
		f := &fakeProcs{tab: shellTree(), ignoreTerm: map[int]bool{200: true}}
		f.install(t)
		f.onSignal = func(pid int, sig syscall.Signal) {
			if pid == 200 && sig == syscall.SIGTERM {
				f.tab[206] = procInfo{PPID: 200, State: 'S', Comm: "cleanup", Pgrp: 200, StartTicks: 5006}
				f.ignoreTerm[206] = true
			}
		}
		if err := StopBackgroundShell("s", 200, 5000); err != nil {
			t.Fatal(err)
		}
		if _, ok := f.tab[206]; ok {
			t.Fatalf("child forked during the stop survived: sent %v", f.sent)
		}
	})

	t.Run("an unrelated group is not taken in", func(t *testing.T) {
		tab := shellTree()
		tab[400] = procInfo{PPID: 1, State: 'S', Comm: "x", Pgrp: 400, StartTicks: 1}
		f := &fakeProcs{tab: tab}
		f.install(t)
		if err := StopBackgroundShell("s", 200, 5000); err != nil {
			t.Fatal(err)
		}
		if f.signalled("terminated:400") || f.signalled("terminated:300") {
			t.Fatalf("signalled outside the shell: %v", f.sent)
		}
	})

	t.Run("refused request signals nothing", func(t *testing.T) {
		f := &fakeProcs{tab: shellTree()}
		f.install(t)
		if err := StopBackgroundShell("s", 300, 10); !errors.Is(err, ErrBGShellGone) {
			t.Fatalf("err = %v", err)
		}
		if len(f.sent) != 0 {
			t.Fatalf("sent %v", f.sent)
		}
	})
}

// pinProcFD against a real process: a pin with the right start time signals it, one with a
// different start time is refused and leaves it alone.
func TestPinProcFDReal(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skip("cannot start sleep:", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	pid := cmd.Process.Pid
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Skip("no /proc:", err)
	}
	pi, _ := procx.Parse(pid, string(b))

	if _, ok := pinProcFD(pid, pi.StartTicks+1); ok {
		t.Fatal("a pin with the wrong start time was accepted")
	}
	h, ok := pinProcFD(pid, pi.StartTicks)
	if !ok {
		t.Skip("pidfd_open unavailable on this kernel")
	}
	defer h.Close()
	if err := h.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if _, err := cmd.Process.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := h.Signal(syscall.SIGTERM); err == nil {
		t.Fatal("a signal through the pin of a reaped process was delivered")
	}
}
