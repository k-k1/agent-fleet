package memoryx

// Prototype (#3102 study, not for merge): run this package's serial tests in several child
// processes of the same test binary.
//
// The tests cannot use t.Parallel: they t.Setenv HOME / CLAUDE_CONFIG_DIR and swap package
// globals. A process boundary isolates both, so the parent lists the tests, hands chunks of them
// to at most N children (each its own TestMain, testguard and environment) and replays each
// child's output in one piece, so `go test -json` / test2json still sees one test at a time.

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

const shardChildEnv = "AF_MEMORYX_SHARD_CHILD"

// shardCount is how many children run at once; 1 (or unset on a 1-CPU box) keeps the serial run.
func shardCount() int {
	if v := os.Getenv("AF_MEMORYX_SHARDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil && n >= 1 {
			return n
		}
	}
	return min(4, runtime.GOMAXPROCS(0))
}

// shardable says whether the parent may fan out: never in a child, and never when a flag asks
// for one artefact from one process (profiles, coverage, fuzzing, listing) or names a subtest.
func shardable() bool {
	if os.Getenv(shardChildEnv) != "" || shardCount() <= 1 {
		return false
	}
	for _, f := range []string{"test.list", "test.coverprofile", "test.cpuprofile", "test.memprofile",
		"test.blockprofile", "test.mutexprofile", "test.trace", "test.fuzz"} {
		if fl := flag.Lookup(f); fl != nil && fl.Value.String() != "" {
			return false
		}
	}
	if fl := flag.Lookup("test.run"); fl != nil && strings.Contains(fl.Value.String(), "/") {
		return false
	}
	return true
}

// shardArgs is os.Args[1:] without -test.run (the parent hands each child its own).
func shardArgs() []string {
	var out []string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		a := strings.TrimLeft(args[i], "-")
		if a == "test.run" {
			i++
			continue
		}
		if strings.HasPrefix(a, "test.run=") {
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func shardChild(args ...string) *exec.Cmd {
	c := exec.Command(os.Args[0], args...)
	c.Env = append(os.Environ(), shardChildEnv+"=1")
	return c
}

// runSharded returns the exit code of the whole run.
func runSharded() int {
	pattern := "."
	if fl := flag.Lookup("test.run"); fl != nil && fl.Value.String() != "" {
		pattern = fl.Value.String()
	}
	listArgs := []string{"-test.list", pattern}
	if fl := flag.Lookup("test.skip"); fl != nil && fl.Value.String() != "" {
		listArgs = append(listArgs, "-test.skip", fl.Value.String())
	}
	out, err := shardChild(listArgs...).Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "memoryx shard: list tests:", err)
		return 1
	}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "Test") {
			names = append(names, l)
		}
	}
	n := shardCount()
	size := max(1, len(names)/(n*6)) // small chunks so a slow one does not leave the others idle
	var chunks [][]string
	for len(names) > 0 {
		k := min(size, len(names))
		chunks = append(chunks, names[:k])
		names = names[k:]
	}
	base := shardArgs()
	failfast := false
	if fl := flag.Lookup("test.failfast"); fl != nil {
		failfast = fl.Value.String() == "true"
	}

	var (
		mu     sync.Mutex
		next   int
		failed bool
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

				args := append(append([]string(nil), base...), "-test.run", "^("+strings.Join(c, "|")+")$")
				cmd := shardChild(args...)
				var buf bytes.Buffer
				cmd.Stdout, cmd.Stderr = &buf, &buf
				runErr := cmd.Run()
				b := trimFinalStatus(buf.Bytes())

				mu.Lock()
				os.Stdout.Write(b)
				if runErr != nil {
					failed = true
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	// test2json needs the run's own status line framed like the testing package frames it.
	mark := ""
	if fl := flag.Lookup("test.v"); fl != nil && fl.Value.String() == "test2json" {
		mark = "\x16"
	}
	if failed {
		fmt.Println(mark + "FAIL")
		return 1
	}
	fmt.Println(mark + "PASS")
	return 0
}

// trimFinalStatus drops the child's closing PASS / FAIL line; the parent prints one for the run.
func trimFinalStatus(b []byte) []byte {
	t := bytes.TrimRight(b, "\n")
	i := bytes.LastIndexByte(t, '\n') + 1
	switch string(bytes.TrimPrefix(t[i:], []byte{0x16})) {
	case "PASS", "FAIL":
		return b[:i]
	}
	return b
}
