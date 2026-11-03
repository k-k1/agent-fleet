package memoryx

// The package's tests run in several child processes of this test binary at once.
//
// They cannot use t.Parallel: they t.Setenv HOME / CLAUDE_CONFIG_DIR and swap package globals,
// and they spend their time in real git processes, so serially the package takes minutes. A
// process boundary isolates both, so the parent lists the tests, hands chunks of them to at most
// shardCount children and replays each child's output in one piece, which keeps `go test -json`
// seeing one test at a time. The parent runs inside testguard.Run; the children inherit its
// guarded environment as helpers.
//
// A child counts as passed only when it exits 0 and its last line is the done line it prints
// after m.Run returned: an exit status alone cannot tell a finished run from a test that called
// os.Exit(0) halfway.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// shardChildEnv marks a child. The parent sets it to "<parent pid>:<nonce>"; the child rewrites
// it to shardNested at once, so its own helper processes neither shard nor print a done line.
const shardChildEnv = "AF_MEMORYX_SHARD_CHILD"

const shardNested = "nested"

// shardsEnv overrides the child count; 1 is the plain serial run.
const shardsEnv = "AF_MEMORYX_SHARDS"

// shardKillGraceMax bounds how long past the run's deadline the parent waits before it kills a
// child: the child gets the remaining time as its own -test.timeout, and its panic names the
// test that hung, which a kill would not.
const shardKillGraceMax = 30 * time.Second

// shardWaitDelay is how long the parent keeps reading a child's output after the child exited;
// a process the child left behind holding the pipe would otherwise block the run forever.
const shardWaitDelay = 5 * time.Second

// shardRunner is the testguard.Runner of a sharded run.
type shardRunner struct{}

func (shardRunner) Run() int { return runSharded(os.Stdout) }

// shardDone is the done line of a direct child; empty in any other process.
var shardDone string

// shardChildStart runs first in TestMain. In a direct child it records the done line and exits
// when the parent is already gone: the parent-death signal is only armed once the child runs,
// so a parent killed before that would otherwise leave the child running.
func shardChildStart() {
	v := os.Getenv(shardChildEnv)
	ppid, nonce, ok := strings.Cut(v, ":")
	if !ok {
		return
	}
	os.Setenv(shardChildEnv, shardNested)
	if strconv.Itoa(os.Getppid()) != ppid {
		fmt.Fprintln(os.Stderr, "memoryx shard: the parent process is gone")
		os.Exit(1)
	}
	shardDone = "memoryx-shard-done " + nonce
}

// shardChildEnd runs after the tests returned; only then may a child report that it finished.
func shardChildEnd() {
	if shardDone != "" {
		fmt.Fprintf(os.Stdout, "\n%s\n", shardDone)
	}
}

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
	return shardableWith(shardFlag, testing.CoverMode(), os.Getenv(shardChildEnv), shardCount())
}

// shardableWith decides from the flag values (flag reads a flag's value by name), the cover
// mode, the child marker and the child count. A -test.run or -test.skip naming a subtest stays
// serial: the parent lists and hands out top-level names only.
func shardableWith(flag func(string) string, coverMode, child string, n int) bool {
	if child != "" || n <= 1 || coverMode != "" {
		return false
	}
	for _, f := range shardSerialFlags {
		if flag(f) != "" {
			return false
		}
	}
	return !strings.Contains(flag("test.run"), "/") && !strings.Contains(flag("test.skip"), "/")
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

// shardSelect is the listed names a run starts. -test.list ignores -test.skip, so the parent
// applies it; without a "/" (shardableWith) it is one unanchored regexp, as in the testing
// package. A Fuzz target's seed corpus and an Example run as tests and are handed out too;
// benchmarks never get here (-test.bench is serial).
func shardSelect(list []byte, skip string) ([]string, error) {
	var re *regexp.Regexp
	if skip != "" {
		var err error
		if re, err = regexp.Compile(skip); err != nil {
			return nil, fmt.Errorf("-test.skip: %v", err)
		}
	}
	var names []string
	for _, l := range strings.Split(string(list), "\n") {
		if !strings.HasPrefix(l, "Test") && !strings.HasPrefix(l, "Fuzz") && !strings.HasPrefix(l, "Example") {
			continue
		}
		if re == nil || !re.MatchString(l) {
			names = append(names, l)
		}
	}
	return names, nil
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

// shardWantRuns is how often each selected test starts: -test.count times per -test.cpu entry.
func shardWantRuns(count, cpu string) int {
	n, err := strconv.Atoi(count)
	if err != nil || n < 0 {
		n = 1
	}
	cpus := 0
	for _, c := range strings.Split(cpu, ",") {
		if strings.TrimSpace(c) != "" {
			cpus++
		}
	}
	return n * max(1, cpus)
}

// shardMissing lists, in the order of names, every listed test that did not run exactly want
// times.
func shardMissing(names []string, runs map[string]int, want int) []string {
	var bad []string
	for _, n := range names {
		if runs[n] != want {
			bad = append(bad, fmt.Sprintf("%s ran %d times, want %d", n, runs[n], want))
		}
	}
	return bad
}

// shardFinished strips the done line from a child's output and reports whether it was there,
// as the last line.
func shardFinished(b []byte, done string) ([]byte, bool) {
	suffix := "\n" + done + "\n"
	if !bytes.HasSuffix(b, []byte(suffix)) {
		return b, false
	}
	return b[:len(b)-len(suffix)], true
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

// shardChild is a child command. It dies with the parent (Pdeathsig fires when the thread that
// started it exits, which is why every caller holds its OS thread until the child is reaped),
// and it is killed when ctx ends.
func shardChild(ctx context.Context, nonce string, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, os.Args[0], args...)
	c.Env = append(os.Environ(), fmt.Sprintf("%s=%d:%s", shardChildEnv, os.Getpid(), nonce))
	c.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	c.WaitDelay = shardWaitDelay
	return c
}

// shardRunChild runs one child and returns its output without the done line, and an error
// unless it exited 0 after its tests returned.
func shardRunChild(ctx context.Context, nonce string, args ...string) ([]byte, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cmd := shardChild(ctx, nonce, args...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	b, done := shardFinished(buf.Bytes(), "memoryx-shard-done "+nonce)
	if err == nil && !done {
		err = fmt.Errorf("exited 0 before its tests returned")
	}
	return b, err
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

	var nb [8]byte
	_, _ = rand.Read(nb[:])
	nonce := hex.EncodeToString(nb[:])

	ctx := context.Background()
	var deadline time.Time
	if d, err := time.ParseDuration(shardFlag("test.timeout")); err == nil && d > 0 {
		deadline = time.Now().Add(d)
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(min(shardKillGraceMax, d/4)))
		defer cancel()
	}

	pattern := shardFlag("test.run")
	if pattern == "" {
		pattern = "."
	}
	out, err := shardRunChild(ctx, nonce, "-test.list", pattern)
	if err != nil {
		return fail("memoryx shard: listing the tests failed: %v\n%s", err, out)
	}
	names, err := shardSelect(out, shardFlag("test.skip"))
	if err != nil {
		return fail("memoryx shard: %v", err)
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
				b, runErr := shardRunChild(ctx, nonce, args...)
				b = trimFinalStatus(b)

				mu.Lock()
				_, _ = w.Write(b)
				if len(b) > 0 && b[len(b)-1] != '\n' {
					fmt.Fprintln(w)
				}
				if runErr != nil {
					// Also for a child that panicked, was killed or exited early without a
					// --- FAIL line: this line is then the only trace of which tests it ran.
					fmt.Fprintf(w, "memoryx shard: child running -test.run %s: %v\n", run, runErr)
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
	// Only a verbose run shows which tests started; there every selected test must have
	// started as often as -test.count and -test.cpu say, or a chunk lost or duplicated one.
	if verbose {
		want := shardWantRuns(shardFlag("test.count"), shardFlag("test.cpu"))
		if bad := shardMissing(names, runs, want); len(bad) > 0 {
			return fail("memoryx shard: the run does not match the listed tests:\n\t%s", strings.Join(bad, "\n\t"))
		}
	}
	if len(names) == 0 {
		fmt.Fprintln(w, "testing: warning: no tests to run")
	}
	fmt.Fprintln(w, mark+"PASS")
	return 0
}
