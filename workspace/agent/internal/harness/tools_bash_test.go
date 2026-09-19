package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBashRunsCommandInCwd(t *testing.T) {
	rt := &Runtime{Cwd: t.TempDir()}
	out, err := runBash(context.Background(), rt, `{"command":"pwd"}`)
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	real, _ := filepath.EvalSymlinks(rt.Cwd)
	gotReal, _ := filepath.EvalSymlinks(strings.TrimSpace(out))
	if gotReal != real {
		t.Fatalf("pwd = %q, want %q", strings.TrimSpace(out), real)
	}
}

func TestBashCapturesStdoutAndStderr(t *testing.T) {
	rt := &Runtime{Cwd: t.TempDir()}
	out, err := runBash(context.Background(), rt, `{"command":"echo out; echo err 1>&2"}`)
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if !strings.Contains(out, "out") || !strings.Contains(out, "err") {
		t.Fatalf("bash output = %q", out)
	}
}

func TestBashRequiresCommand(t *testing.T) {
	rt := &Runtime{Cwd: t.TempDir()}
	if _, err := runBash(context.Background(), rt, `{"command":""}`); err == nil {
		t.Fatal("expected an error for an empty command")
	}
}

// TestBashCancelStopsRunningCommand is the "途中 cancel" requirement (ADR 0093):
// cancelling the loop's ctx must actually kill a bash command already running,
// not just stop the LOOP from asking for more turns.
func TestBashCancelStopsRunningCommand(t *testing.T) {
	rt := &Runtime{Cwd: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runBash(ctx, rt, `{"command":"sleep 30"}`)
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
		// good: the sleep was killed instead of running to completion
	case <-time.After(5 * time.Second):
		t.Fatal("runBash did not stop within 5s of the context being cancelled (sleep 30 not killed)")
	}
}

func TestBashPerCallTimeoutDoesNotKillWholeLoop(t *testing.T) {
	rt := &Runtime{Cwd: t.TempDir()}
	out, err := runBash(context.Background(), rt, `{"command":"sleep 5","timeout_sec":1}`)
	if err != nil {
		t.Fatalf("runBash should report a timeout as output, not an error: %v", err)
	}
	if !strings.Contains(out, "timed out") {
		t.Fatalf("bash output = %q, want a timeout notice", out)
	}
}

func TestRtkRewriteFallsBackWhenUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no rtk on PATH
	got := rtkRewrite(context.Background(), "git status")
	if got != "git status" {
		t.Fatalf("rtkRewrite fallback = %q, want unchanged command", got)
	}
}

func TestRtkRewriteUsesOutputWhenPresent(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "rtk")
	script := "#!/bin/sh\necho rewritten-by-fake-rtk\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	got := rtkRewrite(context.Background(), "git status")
	if got != "rewritten-by-fake-rtk" {
		t.Fatalf("rtkRewrite = %q, want the fake rtk's stdout", got)
	}
}
