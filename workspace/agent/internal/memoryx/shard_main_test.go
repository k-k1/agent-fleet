package memoryx

// The package's tests run in several child processes of this test binary at once.
//
// They cannot use t.Parallel: they t.Setenv HOME / CLAUDE_CONFIG_DIR and swap package globals,
// and they spend their time in real git processes, so serially the package takes minutes. A
// process boundary isolates both, so the parent lists the tests, hands chunks of them to at most
// shardCount children and replays each child's output in one piece, which keeps `go test -json`
// seeing one test at a time. The parent runs inside testguard.Run; the children inherit its
// guarded environment as helpers.

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// shardChildEnv marks a child: it runs its chunk itself and never shards again.
const shardChildEnv = "AF_MEMORYX_SHARD_CHILD"

// shardsEnv overrides the child count; 1 is the plain serial run.
const shardsEnv = "AF_MEMORYX_SHARDS"

// shardKillGrace is how long past the run's deadline the parent waits before it kills a child:
// the child gets the remaining time as its own -test.timeout, and its panic names the test that
// hung, which a kill would not.
const shardKillGrace = 30 * time.Second

// shardRunner is the testguard.Runner of a sharded run.
type shardRunner struct{}

func (shardRunner) Run() int { return runSharded(os.Stdout) }

func shardCount() int {
	if v := os.Getenv(shardsEnv); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			return n
		}
	}
	return min(4, runtime.GOMAXPROCS(0))
}

// shardSerialFlags are the flags whose result is one artefact of one process (profiles,
// coverage, a listing, fuzzing, benchmarks); any of them keeps the run serial.
var shardSerialFlags = []string{"test.list", "test.coverprofile", "test.cpuprofile", "test.memprofile",
	"test.blockprofile", "test.mutexprofile", "test.trace", "test.fuzz", "test.bench"}

func shardable() bool {
	return shardableWith(func(name string) string {
		if f := flag.Lookup(name); f != nil {
			return f.Value.String()
		}
		return ""
	}, testing.CoverMode(), os.Getenv(shardChildEnv), shardCount())
}

// shardableWith decides from the flag values (flag reads a flag's value by name), the cover
// mode, the child marker and the child count. A -test.run naming a subtest stays serial: the
// parent lists and hands out top-level names only.
func shardableWith(flag func(string) string, coverMode, child string, n int) bool {
	if child != "" || n <= 1 || coverMode != "" {
		return false
	}
	for _, f := range shardSerialFlags {
		if flag(f) != "" {
			return false
		}
	}
	return !strings.Contains(flag("test.run"), "/")
}

// shardArgs is args without -test.run and -test.timeout: the parent gives each child its own.
func shardArgs(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		name, _, hasValue := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if name == "test.run" || name == "test.timeout" {
			if !hasValue {
				i++
			}
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// shardChunks splits names, in order, into chunks small enough that one slow chunk does not
// leave the other children idle at the end.
func shardChunks(names []string, n int) [][]string {
	size := max(1, len(names)/(n*6))
	var chunks [][]string
	for len(names) > 0 {
		k := min(size, len(names))
		chunks = append(chunks, names[:k:k])
		names = names[k:]
	}
	return chunks
}

// shardRuns counts the top-level "=== RUN" lines of a verbose child output per test name.
func shardRuns(out []byte, runs map[string]int) {
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimPrefix(l, "\x16")
		if name, ok := strings.CutPrefix(l, "=== RUN"); ok {
			if name = strings.TrimSpace(name); name != "" && !strings.Contains(name, "/") {
				runs[name]++
			}
		}
	}
}

// shardMissing lists, in the order of names, every listed test that did not run exactly count times.
func shardMissing(names []string, runs map[string]int, count int) []string {
	var bad []string
	for _, n := range names {
		if runs[n] != count {
			bad = append(bad, fmt.Sprintf("%s ran %d times, want %d", n, runs[n], count))
		}
	}
	return bad
}

// trimFinalStatus drops a child's closing PASS / FAIL line; the parent prints one for the run.
func trimFinalStatus(b []byte) []byte {
	t := bytes.TrimRight(b, "\n")
	i := bytes.LastIndexByte(t, '\n') + 1
	switch string(bytes.TrimPrefix(t[i:], []byte{0x16})) {
	case "PASS", "FAIL":
		return b[:i]
	}
	return b
}

func shardFlag(name string) string {
	if f := flag.Lookup(name); f != nil {
		return f.Value.String()
	}
	return ""
}

func shardChild(ctx context.Context, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, os.Args[0], args...)
	c.Env = append(os.Environ(), shardChildEnv+"=1")
	return c
}

// runSharded runs the tests in children, writes their output to w and returns the exit code.
func runSharded(w io.Writer) int {
	// Frame the parent's own lines the way the testing package frames its status lines, so
	// test2json reads them as the run's and not as a test's output.
	mark := ""
	verbose := shardFlag("test.v") == "test2json" || shardFlag("test.v") == "true"
	if shardFlag("test.v") == "test2json" {
		mark = "\x16"
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(w, format+"\n", a...)
		fmt.Fprintln(w, mark+"FAIL")
		return 1
	}

	ctx := context.Background()
	var deadline time.Time
	if d, err := time.ParseDuration(shardFlag("test.timeout")); err == nil && d > 0 {
		deadline = time.Now().Add(d)
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(shardKillGrace))
		defer cancel()
	}

	pattern := shardFlag("test.run")
	if pattern == "" {
		pattern = "."
	}
	listArgs := []string{"-test.list", pattern}
	if s := shardFlag("test.skip"); s != "" {
		listArgs = append(listArgs, "-test.skip", s)
	}
	out, err := shardChild(ctx, listArgs...).CombinedOutput()
	if err != nil {
		return fail("memoryx shard: listing the tests failed: %v\n%s", err, out)
	}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		// Benchmarks are kept serial by -test.bench; a Fuzz target's seed corpus and an
		// Example run as tests and must be handed out too.
		if strings.HasPrefix(l, "Test") || strings.HasPrefix(l, "Fuzz") || strings.HasPrefix(l, "Example") {
			names = append(names, l)
		}
	}

	n := shardCount()
	chunks := shardChunks(names, n)
	base := shardArgs(os.Args[1:])
	failfast := shardFlag("test.failfast") == "true"

	var (
		mu     sync.Mutex
		next   int
		failed bool
		runs   = map[string]int{}
		wg     sync.WaitGroup
	)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if next >= len(chunks) || (failed && failfast) {
					mu.Unlock()
					return
				}
				c := chunks[next]
				next++
				mu.Unlock()

				run := "^(" + strings.Join(c, "|") + ")$"
				args := append(append([]string(nil), base...), "-test.run", run)
				if !deadline.IsZero() {
					left := time.Until(deadline)
					if left <= 0 {
						left = time.Millisecond
					}
					args = append(args, "-test.timeout", left.String())
				}
				cmd := shardChild(ctx, args...)
				var buf bytes.Buffer
				cmd.Stdout, cmd.Stderr = &buf, &buf
				runErr := cmd.Run()
				b := trimFinalStatus(buf.Bytes())

				mu.Lock()
				_, _ = w.Write(b)
				if runErr != nil {
					// Also for a child that panicked, was killed or exited without a --- FAIL
					// line: this line is then the only trace of which tests it was running.
					fmt.Fprintf(w, "memoryx shard: child running -test.run %s ended with %v\n", run, runErr)
					failed = true
				}
				shardRuns(b, runs)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if failed {
		fmt.Fprintln(w, mark+"FAIL")
		return 1
	}
	// Only a verbose run shows which tests started; there every listed test must have started
	// exactly -test.count times, or a chunk pattern silently lost or duplicated one.
	if verbose {
		count, err := strconv.Atoi(shardFlag("test.count"))
		if err != nil || count < 1 {
			count = 1
		}
		if bad := shardMissing(names, runs, count); len(bad) > 0 {
			return fail("memoryx shard: the run does not match the listed tests:\n\t%s", strings.Join(bad, "\n\t"))
		}
	}
	if len(names) == 0 {
		fmt.Fprintln(w, "testing: warning: no tests to run")
	}
	fmt.Fprintln(w, mark+"PASS")
	return 0
}
