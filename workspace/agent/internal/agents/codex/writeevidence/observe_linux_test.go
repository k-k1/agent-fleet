package writeevidence

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
)

func TestMain(m *testing.M) { os.Exit(testguard.Run(m, nil)) }

func requireTracer(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("strace"); err != nil {
		t.Skip("strace is unavailable")
	}
	cmd := exec.CommandContext(t.Context(), "strace", "--kill-on-exit", "-e", "trace=none", "--", "/bin/true")
	if out, err := cmd.CombinedOutput(); err != nil {
		for _, unsupported := range []string{"unrecognized option", "invalid option", "Operation not permitted", "Permission denied"} {
			if strings.Contains(string(out), unsupported) {
				t.Skipf("ptrace or kill-on-exit is unavailable: %s", out)
			}
		}
		t.Fatalf("tracer capability probe: %v\n%s", err, out)
	}
}

func TestRunCommittedWriteAndUnrelatedChanges(t *testing.T) {
	requireTracer(t)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "Write Evidence Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Write Evidence Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.invalid")
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", root}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "work")
	for _, name := range []string{"edited.txt", "upstream.txt", "unrelated.txt"} {
		writeFile(t, filepath.Join(root, name), "base\n")
	}
	git("add", ".")
	git("commit", "-qm", "base")
	git("switch", "-qc", "incoming")
	writeFile(t, filepath.Join(root, "upstream.txt"), "incoming\n")
	git("commit", "-qam", "incoming")
	git("switch", "-q", "work")
	writeFile(t, filepath.Join(root, "preexisting.txt"), "outside the command\n")

	coord := t.TempDir()
	scope := Scope{SessionID: "session-a", TurnID: "turn-a"}
	command := shell(root, `
		git merge --ff-only -q incoming
		sh -ec 'printf "edited\n" > edited.txt'
		: > "$1/ready"
		while [ ! -f "$1/resume" ]; do sleep .025; done
		git add edited.txt
		git commit -qm edited
	`, coord)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type result struct {
		evidence Evidence
		err      error
	}
	done := make(chan result, 1)
	go func() {
		evidence, err := Run(ctx, scope, root, command)
		done <- result{evidence, err}
	}()
	waitForFile(t, filepath.Join(coord, "ready"))
	writeFile(t, filepath.Join(root, "unrelated.txt"), "concurrent\n")
	writeFile(t, filepath.Join(coord, "resume"), "")
	got := <-done
	if got.err != nil || got.evidence.Scope != scope || !slices.Equal(got.evidence.Paths, []string{"edited.txt"}) {
		t.Fatalf("Run = %+v, %v; want only edited.txt in %+v", got.evidence, got.err, scope)
	}
	if content := git("show", "HEAD:edited.txt"); content != "edited" {
		t.Fatalf("committed content = %q", content)
	}
	if content := git("show", "HEAD^:upstream.txt"); content != "incoming" {
		t.Fatalf("upstream update did not take effect: %q", content)
	}
	if dirty := git("status", "--porcelain"); dirty != "M unrelated.txt\n?? preexisting.txt" {
		t.Fatalf("unexpected worktree state after the observed turn: %q", dirty)
	}
}

func TestRunCancellationAndFreshObservation(t *testing.T) {
	requireTracer(t)
	root := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	scope := Scope{SessionID: "session-a", TurnID: "same-turn-id"}
	stdout, writer := traceeOutput(t)
	command := shell(root, `printf partial > first; printf 'ready\n'; exec sleep 10`)
	command.Stdout = writer
	done := make(chan error, 1)
	finished := make(chan struct{})
	defer func() {
		cancel()
		<-finished
	}()
	go func() {
		defer close(finished)
		got, err := Run(ctx, scope, root, command)
		if got.Scope != (Scope{}) || len(got.Paths) != 0 {
			err = errors.New("canceled observation returned evidence")
		}
		done <- err
	}()
	assertTraceeReady(t, stdout)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	_ = writer.Close()
	assertTraceeEOF(t, stdout)
	got, err := Run(t.Context(), scope, root, shell(root, `printf fresh > second`))
	if err != nil || got.Scope != scope || !slices.Equal(got.Paths, []string{"second"}) {
		t.Fatalf("fresh observation = %+v, %v", got, err)
	}
	other := Scope{SessionID: "session-b", TurnID: scope.TurnID}
	got, err = Run(t.Context(), other, root, shell(root, `printf separate > third`))
	if err != nil || got.Scope != other || !slices.Equal(got.Paths, []string{"third"}) {
		t.Fatalf("separate session = %+v, %v", got, err)
	}
}

func TestRunBudgetsStopTracees(t *testing.T) {
	requireTracer(t)
	for _, tc := range []struct {
		name, script string
		duration     time.Duration
		bytes        int64
	}{
		{"trace bytes", `i=0; while [ "$i" -lt 512 ]; do printf x >> many; i=$((i+1)); done; exec sleep 10`, maxDuration, 16 << 10},
		{"elapsed time", `exec sleep 10`, 2 * time.Second, maxTrace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			stdout, writer := traceeOutput(t)
			command := shell(root, `printf 'ready\n'; `+tc.script)
			command.Stdout = writer
			paths, err := observe(t.Context(), root, command, tc.duration, tc.bytes)
			_ = writer.Close()
			if !errors.Is(err, ErrLimit) || len(paths) != 0 {
				t.Fatalf("observe = %v, %v; want no paths and ErrLimit", paths, err)
			}
			assertTraceeReady(t, stdout)
			assertTraceeEOF(t, stdout)
		})
	}
}

func TestRunDiscardEvidenceOnCommandFailure(t *testing.T) {
	requireTracer(t)
	root := t.TempDir()
	got, err := Run(t.Context(), Scope{"s", "t"}, root, shell(root, `printf x > written; exit 3`))
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 || got.Scope != (Scope{}) || len(got.Paths) != 0 {
		t.Fatalf("Run = %+v, %v; want exit code 3 and no evidence", got, err)
	}
	assertContents(t, filepath.Join(root, "written"), "x")
}

func TestRunnerCrashStopsTracees(t *testing.T) {
	const childRoot = "AF_TEST_WRITE_EVIDENCE_OWNER_ROOT"
	if root := os.Getenv(childRoot); root != "" {
		command := shell(root, `sh -ec 'printf "ready\n"; exec sleep 10' & wait`)
		command.Stdout = os.Stdout
		_, err := Run(t.Context(), Scope{"s", "t"}, root, command)
		t.Fatalf("runner should have been killed while tracees were alive, Run returned %v", err)
	}
	requireTracer(t)
	stdout, writer := traceeOutput(t)
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRunnerCrashStopsTracees$")
	cmd.Env = append(os.Environ(), childRoot+"="+t.TempDir(), "TMPDIR="+t.TempDir())
	cmd.Stdout = writer
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	defer cmd.Process.Kill()
	_ = writer.Close()
	assertTraceeReady(t, stdout)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	assertTraceeEOF(t, stdout)
}

func TestRunPathsAndStreamIsolation(t *testing.T) {
	requireTracer(t)
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(outside, "tool-output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	name := "a > \n\xff.txt"
	command := shell(root, `
		test ! -e /proc/self/fd/3
		printf 'trace-like text\n' >&2
		printf test > "$1"
		printf outside > link/target
		: > empty
		exec 4>>append-open
	`, name)
	command.Stderr = output
	got, err := Run(t.Context(), Scope{"s", "t"}, root, command)
	if err != nil || !slices.Equal(got.Paths, []string{name, "empty"}) {
		t.Fatalf("Run = %+v, %v", got, err)
	}
	assertContents(t, output.Name(), "trace-like text\n")
	assertContents(t, filepath.Join(outside, "target"), "outside")
	assertContents(t, filepath.Join(root, "append-open"), "")
}

func BenchmarkShellWrites(b *testing.B) {
	requireTracer(b)
	root := b.TempDir()
	command := shell(root, `i=0; while [ "$i" -lt 100 ]; do printf text >> output; i=$((i+1)); done`)
	b.Run("direct", func(b *testing.B) {
		for b.Loop() {
			cmd := exec.CommandContext(b.Context(), command.Path, command.Args...)
			cmd.Dir = root
			if err := cmd.Run(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("observed", func(b *testing.B) {
		for b.Loop() {
			got, err := Run(b.Context(), Scope{"s", "t"}, root, command)
			if err != nil || !slices.Equal(got.Paths, []string{"output"}) {
				b.Fatalf("Run = %+v, %v", got, err)
			}
		}
	})
}

func shell(root, script string, args ...string) Command {
	return Command{Path: "/bin/sh", Dir: root, Args: append([]string{"-ec", script, "sh"}, args...)}
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func assertContents(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("read %s = %q, %v; want %q", path, data, err, want)
	}
}

func traceeOutput(t *testing.T) (*bufio.Reader, *os.File) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = writer.Close()
		_ = reader.Close()
	})
	if err := reader.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return bufio.NewReader(reader), writer
}

func assertTraceeReady(t *testing.T, stdout *bufio.Reader) {
	t.Helper()
	if line, err := stdout.ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("tracee readiness = %q, %v", line, err)
	}
}

func assertTraceeEOF(t *testing.T, stdout *bufio.Reader) {
	t.Helper()
	// The inherited stdout remains open until the tracer and its tracees have closed it.
	if _, err := stdout.ReadByte(); err != io.EOF {
		t.Fatalf("tracees retained stdout: %v", err)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
