package memoryx

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// shardProbeEnv makes the TestShardProbe* tests misbehave; unset, they pass at once. Only the
// sharded runs started by the tests below set it.
const shardProbeEnv = "AF_MEMORYX_SHARD_PROBE"

// shardProbePidsEnv names a file the pipehold probe appends its leftover process's pid to, so the
// test that started it can kill it.
const shardProbePidsEnv = "AF_MEMORYX_SHARD_PROBE_PIDS"

// shardProbeHold is how long a stuck probe would hold the run up (a hanging test, a leftover
// process keeping the pipe open). The runner must end the run in well under it: a fixed
// wall-clock bound fails on a slow host, where -race alone makes each sharded probe run take
// 2-4 s of process start-up (measured on a CI runner and locally).
const shardProbeHold = time.Minute

func TestShardProbeA(t *testing.T) {}

func TestShardProbeB(t *testing.T) {
	switch os.Getenv(shardProbeEnv) {
	case "fail":
		t.Fatal("shard probe failure")
	case "panic":
		panic("shard probe panic")
	case "exit":
		os.Exit(3)
	case "exit0":
		os.Exit(0)
	case "hang":
		time.Sleep(shardProbeHold)
	case "stop":
		shardProbeStopSelf(0)
	case "stop-late":
		shardProbeStopSelf(200 * time.Millisecond)
	case "pipehold":
		// A process left behind that keeps the child's output pipe open after the child exits.
		cmd := exec.Command("sleep", strconv.Itoa(int(shardProbeHold/time.Second)))
		cmd.Stdout = os.Stdout
		if cmd.Start() == nil {
			if f, err := os.OpenFile(os.Getenv(shardProbePidsEnv), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
				fmt.Fprintln(f, cmd.Process.Pid)
				f.Close()
			}
		}
	}
}

// shardProbeStopSelf stops this process and never returns. Stopped, the child cannot even run
// its own -test.timeout; only the parent's kill ends it. kill(2) on one's own pid may return
// before the stop takes effect when another thread dequeues the signal, so returning from here
// would let the probe run on to PASS (seen once in CI: exit 0 after 0.07 s). Blocking makes the
// outcome independent of when the stop lands; delay forces the late landing in a test.
func shardProbeStopSelf(delay time.Duration) {
	go func() {
		time.Sleep(delay)
		_ = syscall.Kill(os.Getpid(), syscall.SIGSTOP)
	}()
	time.Sleep(shardProbeHold)
}

func TestShardProbeC(t *testing.T) {}

var shardProbes = []string{"TestShardProbeA", "TestShardProbeB", "TestShardProbeC"}

// shardProbeCmd is this binary as a sharded parent over the probes, two children at a time. The
// child marker of this process (when it is a child itself) is dropped so that it shards. Flags in
// extra come last and so win over the defaults.
func shardProbeCmd(mode string, extra ...string) *exec.Cmd {
	args := append([]string{"-test.run", "^(" + strings.Join(shardProbes, "|") + ")$", "-test.v=test2json",
		"-test.count=1", "-test.timeout=2m"}, extra...)
	cmd := exec.Command(os.Args[0], args...)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, shardChildEnv+"=") && !strings.HasPrefix(e, shardsEnv+"=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, shardsEnv+"=2", shardProbeEnv+"="+mode)
	return cmd
}

func runShardedProbes(t *testing.T, mode string, extra ...string) (string, int) {
	t.Helper()
	cmd := shardProbeCmd(mode, extra...)
	pids := filepath.Join(t.TempDir(), "pids")
	cmd.Env = append(cmd.Env, shardProbePidsEnv+"="+pids)
	t.Cleanup(func() {
		b, _ := os.ReadFile(pids)
		for _, s := range strings.Fields(string(b)) {
			if pid, err := strconv.Atoi(s); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run: %v", err)
	}
	return buf.String(), code
}

func shardLastLine(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	return lines[len(lines)-1]
}

func shardStatusLines(out string) int {
	n := 0
	for _, l := range strings.Split(out, "\n") {
		if s := strings.TrimPrefix(l, "\x16"); s == "PASS" || s == "FAIL" {
			n++
		}
	}
	return n
}

func TestShardedRunPassesAndRunsEveryTestOnce(t *testing.T) {
	out, code := runShardedProbes(t, "")
	if code != 0 || shardLastLine(out) != "\x16PASS" {
		t.Fatalf("exit %d, last line %q, want 0 and a framed PASS:\n%s", code, shardLastLine(out), out)
	}
	runs := map[string]int{}
	shardRuns([]byte(out), runs)
	if want := map[string]int{"TestShardProbeA": 1, "TestShardProbeB": 1, "TestShardProbeC": 1}; !reflect.DeepEqual(runs, want) {
		t.Fatalf("runs = %v, want %v:\n%s", runs, want, out)
	}
	if n := shardStatusLines(out); n != 1 {
		t.Errorf("%d status lines, want each child's dropped and one for the run:\n%s", n, out)
	}
	if strings.Contains(out, "memoryx-shard-done") {
		t.Errorf("a child's done line reached the output:\n%s", out)
	}
}

// A child that fails, panics, exits (with any status) or hangs fails the whole run, and the
// output names the test.
func TestShardedRunReportsAFailingChild(t *testing.T) {
	for _, c := range []struct {
		mode  string
		extra []string
		want  string
	}{
		{"fail", nil, "--- FAIL: TestShardProbeB"},
		{"panic", nil, "panic: shard probe panic"},
		{"exit", nil, "-test.run ^(TestShardProbeB)$: exit status 3"},
		{"exit0", nil, "-test.run ^(TestShardProbeB)$: exited 0 before its tests returned"},
		{"exit0", []string{"-test.v=false"}, "-test.run ^(TestShardProbeB)$: exited 0 before its tests returned"},
		{"hang", []string{"-test.timeout=3s"}, "test timed out after"},
		{"stop", []string{"-test.timeout=2s"}, "-test.run ^(TestShardProbeB)$: signal: killed"},
		{"stop-late", []string{"-test.timeout=2s"}, "-test.run ^(TestShardProbeB)$: signal: killed"},
		{"pipehold", nil, "-test.run ^(TestShardProbeB)$: exec: WaitDelay expired"},
	} {
		t.Run(c.mode+strings.Join(c.extra, ""), func(t *testing.T) {
			start := time.Now()
			out, code := runShardedProbes(t, c.mode, c.extra...)
			if code != 1 || !strings.HasSuffix(shardLastLine(out), "FAIL") {
				t.Fatalf("exit %d, last line %q, want 1 and FAIL:\n%s", code, shardLastLine(out), out)
			}
			if !strings.Contains(out, c.want) {
				t.Fatalf("output lacks %q:\n%s", c.want, out)
			}
			if d := time.Since(start); d > shardProbeHold/2 {
				t.Errorf("the run took %v; a stuck child must not hold it up for %v", d, shardProbeHold)
			}
		})
	}
}

// -test.list ignores -test.skip, so the parent must apply it, or a skipped test reads as lost.
func TestShardedRunHonoursSkip(t *testing.T) {
	for _, v := range []string{"-test.v=false", "-test.v=true", "-test.v=test2json"} {
		t.Run(v, func(t *testing.T) {
			out, code := runShardedProbes(t, "fail", v, "-test.skip", "TestShardProbeB")
			if code != 0 || strings.Contains(out, "TestShardProbeB") {
				t.Fatalf("exit %d, want 0 with TestShardProbeB skipped:\n%s", code, out)
			}
		})
	}
}

// -test.cpu runs every test once per entry and -test.count=0 runs none; neither is a lost test.
func TestShardedRunHonoursCountAndCPU(t *testing.T) {
	for _, c := range []struct {
		flags []string
		want  int
	}{
		{[]string{"-test.cpu=1,2"}, 2},
		{[]string{"-test.cpu=1,2", "-test.count=2"}, 4},
		{[]string{"-test.count=0"}, 0},
	} {
		t.Run(strings.Join(c.flags, " "), func(t *testing.T) {
			out, code := runShardedProbes(t, "", c.flags...)
			if code != 0 {
				t.Fatalf("exit %d, want 0:\n%s", code, out)
			}
			runs := map[string]int{}
			shardRuns([]byte(out), runs)
			for _, p := range shardProbes {
				if runs[p] != c.want {
					t.Errorf("%s ran %d times, want %d", p, runs[p], c.want)
				}
			}
		})
	}
}

// A killed parent takes its children with it (Pdeathsig): a go test interrupted or OOM-killed
// must not leave tests running in a guard root whose owner is gone.
func TestShardChildrenDieWithTheParent(t *testing.T) {
	cmd := shardProbeCmd("hang", "-test.run", "^TestShardProbeB$")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	var child int
	for deadline := time.Now().Add(20 * time.Second); child == 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no shard child running TestShardProbeB appeared")
		}
		for _, pid := range procChildren(cmd.Process.Pid) {
			if b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); bytes.Contains(b, []byte("^(TestShardProbeB)$")) {
				child = pid
			}
		}
	}
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })

	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	for deadline := time.Now().Add(10 * time.Second); procAlive(child); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("shard child %d outlived its killed parent", child)
		}
	}
}

// A child whose parent is already gone when it starts (the window before the parent-death
// signal is armed) exits without running anything.
func TestShardChildExitsWhenItsParentIsGone(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run", "^TestShardProbeA$")
	cmd.Env = append(os.Environ(), shardChildEnv+"=1:feedface")
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 || !strings.Contains(string(out), "parent process is gone") {
		t.Fatalf("err %v, want exit 1 for an orphaned child:\n%s", err, out)
	}
}

func procChildren(pid int) []int {
	var out []int
	tasks, _ := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", pid))
	for _, f := range tasks {
		b, _ := os.ReadFile(f)
		for _, s := range strings.Fields(string(b)) {
			if n, err := strconv.Atoi(s); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

// procAlive is false for a process that is gone or a zombie (dead, not yet reaped by whoever
// inherited it).
func procAlive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	_, rest, ok := strings.Cut(string(b), ") ")
	return ok && !strings.HasPrefix(rest, "Z") && !strings.HasPrefix(rest, "X")
}

func TestShardableKeepsSingleArtefactRunsSerial(t *testing.T) {
	flags := func(kv ...string) func(string) string {
		return func(name string) string {
			for i := 0; i+1 < len(kv); i += 2 {
				if kv[i] == name {
					return kv[i+1]
				}
			}
			return ""
		}
	}
	if !shardableWith(flags("test.run", "TestA|TestB", "test.skip", "TestC", "test.count", "3"), "", "", 4) {
		t.Error("a plain run must shard")
	}
	for _, c := range []struct {
		name  string
		flag  func(string) string
		cover string
		child string
		n     int
	}{
		{"child", flags(), "", "1:abc", 4},
		{"nested", flags(), "", shardNested, 4},
		{"one shard", flags(), "", "", 1},
		{"cover", flags(), "set", "", 4},
		{"subtest run pattern", flags("test.run", "TestA/sub"), "", "", 4},
		{"subtest skip pattern", flags("test.skip", "TestA/sub"), "", "", 4},
		{"list", flags("test.list", "."), "", "", 4},
		{"coverprofile", flags("test.coverprofile", "c.out"), "", "", 4},
		{"cpuprofile", flags("test.cpuprofile", "c.prof"), "", "", 4},
		{"memprofile", flags("test.memprofile", "m.prof"), "", "", 4},
		{"blockprofile", flags("test.blockprofile", "b.prof"), "", "", 4},
		{"mutexprofile", flags("test.mutexprofile", "m.prof"), "", "", 4},
		{"trace", flags("test.trace", "t.out"), "", "", 4},
		{"fuzz", flags("test.fuzz", "FuzzX"), "", "", 4},
		{"bench", flags("test.bench", "."), "", "", 4},
	} {
		if shardableWith(c.flag, c.cover, c.child, c.n) {
			t.Errorf("%s: sharded, want serial", c.name)
		}
	}
}

func TestShardArgsDropsRunAndTimeout(t *testing.T) {
	got := shardArgs([]string{"-test.v=test2json", "-test.run", "TestA", "-test.timeout=20m", "--test.run=X",
		"-test.timeout", "1m", "-test.count=1", "-test.paniconexit0", "-test.skip", "TestB"})
	want := []string{"-test.v=test2json", "-test.count=1", "-test.paniconexit0", "-test.skip", "TestB"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shardArgs = %q, want %q", got, want)
	}
}

func TestShardSelect(t *testing.T) {
	list := []byte("TestA\nTestAB\nFuzzC\nExampleD\nBenchmarkE\n\n")
	for _, c := range []struct {
		skip string
		want []string
	}{
		{"", []string{"TestA", "TestAB", "FuzzC", "ExampleD"}},
		{"TestA", []string{"FuzzC", "ExampleD"}},
		{"^TestA$|Example", []string{"TestAB", "FuzzC"}},
	} {
		got, err := shardSelect(list, c.skip)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("shardSelect(skip %q) = %q, %v; want %q", c.skip, got, err, c.want)
		}
	}
	if _, err := shardSelect(list, "("); err == nil {
		t.Error("a bad -test.skip must be an error")
	}
}

func TestShardWantRuns(t *testing.T) {
	for _, c := range []struct {
		count, cpu string
		want       int
	}{{"1", "", 1}, {"3", "", 3}, {"0", "", 0}, {"2", "1,2,4", 6}, {"", "", 1}, {"1", "8", 1}} {
		if got := shardWantRuns(c.count, c.cpu); got != c.want {
			t.Errorf("shardWantRuns(%q, %q) = %d, want %d", c.count, c.cpu, got, c.want)
		}
	}
}

func TestShardFinished(t *testing.T) {
	const done = "memoryx-shard-done 00"
	for _, c := range []struct {
		in, out string
		ok      bool
	}{
		{"PASS\n\n" + done + "\n", "PASS\n", true},
		{"partial\n" + done + "\n", "partial", true},
		{"PASS\n", "PASS\n", false},
		{"\n" + done + "\nlater\n", "\n" + done + "\nlater\n", false},
		{"\nmemoryx-shard-done 01\n", "\nmemoryx-shard-done 01\n", false},
	} {
		got, ok := shardFinished([]byte(c.in), done)
		if string(got) != c.out || ok != c.ok {
			t.Errorf("shardFinished(%q) = %q, %v; want %q, %v", c.in, got, ok, c.out, c.ok)
		}
	}
}

func TestTrimFinalStatus(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"--- PASS: TestA (0.00s)\nPASS\n", "--- PASS: TestA (0.00s)\n"},
		{"\x16--- FAIL: TestA (0.00s)\n\x16FAIL\n", "\x16--- FAIL: TestA (0.00s)\n"},
		{"PASS\n", ""},
		{"panic: boom\n\ngoroutine 1 [running]:\n", "panic: boom\n\ngoroutine 1 [running]:\n"},
		{"ok PASS\n", "ok PASS\n"},
		{"", ""},
	} {
		if got := string(trimFinalStatus([]byte(c.in))); got != c.want {
			t.Errorf("trimFinalStatus(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShardChunksHandEveryNameOutOnce(t *testing.T) {
	for _, total := range []int{0, 1, 3, 23, 24, 25, 384} {
		var names []string
		for i := range total {
			names = append(names, "Test"+strings.Repeat("x", i))
		}
		chunks := shardChunks(names, 4)
		if got := slices.Concat(chunks...); !slices.Equal(got, names) {
			t.Fatalf("%d names: chunks %v do not concatenate back to the names", total, chunks)
		}
		if total >= 24 && len(chunks) < 24 {
			t.Errorf("%d names: %d chunks, want at least 24 for 4 children", total, len(chunks))
		}
	}
}

func TestShardMissingCatchesLostAndDuplicatedTests(t *testing.T) {
	runs := map[string]int{}
	shardRuns([]byte("\x16=== RUN   TestA\n=== RUN   TestA/sub\n\x16=== RUN   TestC\n=== RUN   TestC\n"), runs)
	got := shardMissing([]string{"TestA", "TestB", "TestC"}, runs, 1)
	want := []string{"TestB ran 0 times, want 1", "TestC ran 2 times, want 1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shardMissing = %q, want %q", got, want)
	}
}
