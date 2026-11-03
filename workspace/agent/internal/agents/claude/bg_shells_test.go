package claude

import (
	"errors"
	"strconv"
	"syscall"
	"testing"
	"time"
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

func TestBgStopTargets(t *testing.T) {
	t.Run("the shell, its subtree and its group", func(t *testing.T) {
		ts, group, err := bgStopTargets(100, shellTree(), 200, 5000)
		if err != nil || !group || len(ts) != 2 || ts[0] != (bgTarget{200, 5000}) || ts[1] != (bgTarget{201, 5001}) {
			t.Fatalf("ts=%+v group=%v err=%v", ts, group, err)
		}
	})
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
			if _, _, err := bgStopTargets(100, shellTree(), c.pid, c.start); !errors.Is(err, ErrBGShellGone) {
				t.Fatalf("err = %v, want ErrBGShellGone", err)
			}
		})
	}
}

// fakeProcs replaces the process table, the kill call and the pane lookup. kill applies
// SIGTERM/SIGKILL to the table according to which pids ignore SIGTERM.
type fakeProcs struct {
	tab        map[int]procInfo
	ignoreTerm map[int]bool
	sent       []string
}

func (f *fakeProcs) install(t *testing.T) {
	t.Helper()
	oKill, oFresh, oRoot, oGrace := killProc, freshProcs, paneRoot, bgStopGrace
	t.Cleanup(func() { killProc, freshProcs, paneRoot, bgStopGrace = oKill, oFresh, oRoot, oGrace })
	bgStopGrace = 300 * time.Millisecond
	paneRoot = func(string) int { return 100 }
	freshProcs = func() map[int]procInfo {
		cp := map[int]procInfo{}
		for k, v := range f.tab {
			cp[k] = v
		}
		return cp
	}
	killProc = func(pid int, sig syscall.Signal) error {
		f.sent = append(f.sent, sig.String()+":"+strconv.Itoa(pid))
		if pid < 0 {
			return nil
		}
		if sig == syscall.SIGKILL || (sig == syscall.SIGTERM && !f.ignoreTerm[pid]) {
			delete(f.tab, pid)
		}
		return nil
	}
}

func TestStopBackgroundShell(t *testing.T) {
	t.Run("SIGTERM is enough", func(t *testing.T) {
		f := &fakeProcs{tab: shellTree()}
		f.install(t)
		if err := StopBackgroundShell("s", 200, 5000); err != nil {
			t.Fatal(err)
		}
		if _, ok := f.tab[200]; ok {
			t.Fatal("shell still alive")
		}
		want := []string{"terminated:-200", "terminated:201", "terminated:200"}
		if len(f.sent) != len(want) {
			t.Fatalf("sent %v, want %v", f.sent, want)
		}
		for i := range want {
			if f.sent[i] != want[i] {
				t.Fatalf("sent %v, want %v", f.sent, want)
			}
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

	t.Run("a reused pid is never signalled", func(t *testing.T) {
		f := &fakeProcs{tab: shellTree(), ignoreTerm: map[int]bool{201: true}}
		f.install(t)
		// During the grace 201 exits and its pid goes to another process.
		orig := killProc
		killProc = func(pid int, sig syscall.Signal) error {
			err := orig(pid, sig)
			if pid == 201 && sig == syscall.SIGTERM {
				f.tab[201] = procInfo{PPID: 1, State: 'S', Comm: "other", StartTicks: 9999}
			}
			return err
		}
		if err := StopBackgroundShell("s", 200, 5000); err != nil {
			t.Fatal(err)
		}
		for _, s := range f.sent {
			if s == "killed:201" {
				t.Fatalf("SIGKILL reached the reused pid: %v", f.sent)
			}
		}
		if f.tab[201].Comm != "other" {
			t.Fatal("unrelated process was killed")
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
