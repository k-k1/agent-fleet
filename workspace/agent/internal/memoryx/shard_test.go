package memoryx

import (
	"bytes"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// shardProbeEnv makes the TestShardProbe* tests misbehave; unset, they pass at once. Only the
// sharded runs started by the tests below set it.
const shardProbeEnv = "AF_MEMORYX_SHARD_PROBE"

func TestShardProbeA(t *testing.T) {}

func TestShardProbeB(t *testing.T) {
	switch os.Getenv(shardProbeEnv) {
	case "fail":
		t.Fatal("shard probe failure")
	case "panic":
		panic("shard probe panic")
	case "exit":
		os.Exit(3)
	case "hang":
		time.Sleep(time.Minute)
	}
}

func TestShardProbeC(t *testing.T) {}

var shardProbes = []string{"TestShardProbeA", "TestShardProbeB", "TestShardProbeC"}

// runShardedProbes runs this binary as a sharded parent over the probes, two children at a time.
// The child marker of this process (when it is a child itself) is dropped so that it shards.
func runShardedProbes(t *testing.T, mode string, extra ...string) (string, int) {
	t.Helper()
	args := append([]string{"-test.run", "^(" + strings.Join(shardProbes, "|") + ")$", "-test.v=test2json",
		"-test.count=1", "-test.timeout=2m"}, extra...)
	cmd := exec.Command(os.Args[0], args...)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, shardChildEnv+"=") && !strings.HasPrefix(e, shardsEnv+"=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, shardsEnv+"=2", shardProbeEnv+"="+mode)
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
	status := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimPrefix(l, "\x16") == "PASS" {
			status++
		}
	}
	if status != 1 {
		t.Errorf("%d status lines, want each child's dropped and one for the run:\n%s", status, out)
	}
}

// A child that fails, panics, exits or hangs fails the whole run, and its output names the test.
func TestShardedRunReportsAFailingChild(t *testing.T) {
	for _, c := range []struct {
		mode  string
		extra []string
		want  string
	}{
		{"fail", nil, "--- FAIL: TestShardProbeB"},
		{"panic", nil, "panic: shard probe panic"},
		{"exit", nil, "-test.run ^(TestShardProbeB)$ ended with exit status 3"},
		{"hang", []string{"-test.timeout=3s"}, "test timed out after"},
	} {
		t.Run(c.mode, func(t *testing.T) {
			out, code := runShardedProbes(t, c.mode, c.extra...)
			if code != 1 || shardLastLine(out) != "\x16FAIL" {
				t.Fatalf("exit %d, last line %q, want 1 and a framed FAIL:\n%s", code, shardLastLine(out), out)
			}
			if !strings.Contains(out, c.want) {
				t.Fatalf("output lacks %q:\n%s", c.want, out)
			}
			if !strings.Contains(out, "TestShardProbeB") {
				t.Fatalf("output does not name the test:\n%s", out)
			}
		})
	}
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
	if !shardableWith(flags("test.run", "TestA|TestB", "test.count", "3"), "", "", 4) {
		t.Error("a plain run must shard")
	}
	for _, c := range []struct {
		name  string
		flag  func(string) string
		cover string
		child string
		n     int
	}{
		{"child", flags(), "", "1", 4},
		{"one shard", flags(), "", "", 1},
		{"cover", flags(), "set", "", 4},
		{"subtest pattern", flags("test.run", "TestA/sub"), "", "", 4},
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
