//go:build unix

package copilot

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO at events.jsonl's path must not stall the turn: TurnWatch runs before session/prompt
// with the lifecycle lease held. Both the mark and the outcome read have to return without a
// writer, including when the path became a FIFO between the two.
func TestEventsNonRegularSourceDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "events.jsonl")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("no FIFO here: %v", err)
	}
	// If the code under test blocks in open, this releases it so the goroutine does not leak.
	t.Cleanup(func() {
		if f, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0); err == nil {
			f.Close()
		}
	})
	within := func(name string, fn func()) {
		t.Helper()
		done := make(chan struct{})
		go func() { fn(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s blocked on a FIFO", name)
		}
	}
	within("takeEventsMark", func() {
		if m := takeEventsMark(fifo); m.ok {
			t.Errorf("a FIFO produced a mark: %+v", m)
		}
	})
	within("failureSince", func() {
		if got := failureSince(fifo, eventsMark{}); got != "" {
			t.Errorf("a FIFO produced a failure: %q", got)
		}
	})

	// Marked as a regular file, then swapped for a FIFO before the outcome is read.
	reg := filepath.Join(dir, "swap.jsonl")
	if err := os.WriteFile(reg, []byte(`{"type":"session.start","data":{}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mark := takeEventsMark(reg)
	if !mark.ok {
		t.Fatal("no mark for a regular file")
	}
	if err := os.Remove(reg); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(reg, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if f, err := os.OpenFile(reg, os.O_RDWR|syscall.O_NONBLOCK, 0); err == nil {
			f.Close()
		}
	})
	within("failureSince after swap", func() {
		if got := failureSince(reg, mark); got != "" {
			t.Errorf("swapped FIFO produced a failure: %q", got)
		}
	})
}
