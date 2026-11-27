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
	defer cancel()
	scope := Scope{SessionID: "session-a", TurnID: "same-turn-id"}
	command := shell(root, `printf partial > first; exec sleep 30`)
	done := make(chan error, 1)
	go func() {
		got, err := Run(ctx, scope, root, command)
		if got.Scope != (Scope{}) || len(got.Paths) != 0 {
			err = errors.New("canceled observation returned evidence")
		}
		done <- err
	}()
	waitForFile(t, filepath.Join(root, "first"))
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
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

func TestRunTraceLimit(t *testing.T) {
	requireTracer(t)
	root := t.TempDir()
	paths, err := observe(t.Context(), root, shell(root, `while :; do printf x >> many; done`), 4096)
	if !errors.Is(err, ErrLimit) || len(paths) != 0 {
		t.Fatalf("overflow = %v, %v; want no paths and ErrLimit", paths, err)
	}
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
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if err := reader.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRunnerCrashStopsTracees$")
	cmd.Env = append(os.Environ(), childRoot+"="+t.TempDir(), "TMPDIR="+t.TempDir())
	cmd.Stdout = writer
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	defer cmd.Process.Kill()
	_ = writer.Close()
	stdout := bufio.NewReader(reader)
	if line, err := stdout.ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("tracee readiness = %q, %v", line, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	// The inherited stdout remains open until both the tracer and its tracees die.
	if _, err := stdout.ReadByte(); err != io.EOF {
		t.Fatalf("tracees retained stdout after runner was killed: %v", err)
	}
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
		exec 4>>read-only-open
	`, name)
	command.Stderr = output
	got, err := Run(t.Context(), Scope{"s", "t"}, root, command)
	if err != nil || !slices.Equal(got.Paths, []string{name, "empty"}) {
		t.Fatalf("Run = %+v, %v", got, err)
	}
	data, err := os.ReadFile(output.Name())
	if err != nil || string(data) != "trace-like text\n" {
		t.Fatalf("tool stderr = %q, %v", data, err)
	}
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
