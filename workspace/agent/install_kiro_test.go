package main

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

// TestKiroAsset pins the arch → asset mapping: x86_64 gnu (Debian 12 glibc suffices),
// aarch64 musl (avoids the gnu build's glibc 2.39 floor). Only the running arch is
// asserted concretely; the others are covered by the switch compiling.
func TestKiroAsset(t *testing.T) {
	got, err := kiroAsset()
	if err != nil {
		t.Fatalf("kiroAsset: %v", err)
	}
	want := map[string]string{
		"amd64": "kirocli-x86_64-linux.zip",
		"arm64": "kirocli-aarch64-linux-musl.zip",
	}[runtime.GOARCH]
	if want != "" && got != want {
		t.Errorf("kiroAsset()=%q, want %q", got, want)
	}
}

// TestInstallKiroIdempotentSkip: an already-present kiro-cli short-circuits before any
// download. A killed prior install that left a broken kiro-cli would be treated as
// "installed" here — that is exactly why the real install now places kiro-cli LAST via
// atomic rename, so this fast path only ever sees a fully-installed binary.
func TestInstallKiroIdempotentSkip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A fake kiro-cli that swallows `settings …` (pinKiroSettings) and exits 0.
	fake := filepath.Join(binDir, "kiro-cli")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := installKiro(); err != nil {
		t.Fatalf("installKiro with present binary should skip cleanly, got %v", err)
	}
}

// TestKiroInstallLockExcludes verifies the cross-process guard (B-2): while the lock
// is held, an independent flock attempt on the same file (a stand-in for a second
// pane's `workspace-agent install-kiro`) cannot acquire it; after release it can.
func TestKiroInstallLockExcludes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	unlock, err := kiroInstallLock()
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}

	// A separate open file description must fail a non-blocking exclusive lock.
	f2, err := os.OpenFile(kiroInstallLockPath(), os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	defer f2.Close()
	if err := syscall.Flock(int(f2.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		_ = syscall.Flock(int(f2.Fd()), syscall.LOCK_UN)
		t.Fatal("second flock acquired while first is held; installs are not serialised")
	}

	unlock()

	// After release the lock is free again.
	if err := syscall.Flock(int(f2.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	_ = syscall.Flock(int(f2.Fd()), syscall.LOCK_UN)
}
