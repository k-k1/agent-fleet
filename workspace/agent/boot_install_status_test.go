package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReadBootInstallStatus(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.tsv")
	body := "claude\tfailed\tno_space\n" +
		"codex\tok\t\n" +
		"rtk\tskipped\n" +
		"agy\tfailed\tbogus\n" + // unknown reason degrades to other
		"cursor\tweird\t\n" + // unknown status is dropped
		"copilot\tfailed\tnetwork\r\n" +
		"claude\tfailed\tchecksum\n" + // later line wins
		"\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	old := bootInstallStatusPath
	bootInstallStatusPath = p
	t.Cleanup(func() { bootInstallStatusPath = old })

	got := readBootInstallStatus()
	want := map[string]bootInstall{
		"claude":  {"failed", "checksum"},
		"codex":   {"ok", ""},
		"rtk":     {"skipped", ""},
		"agy":     {"failed", "other"},
		"copilot": {"failed", "network"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: got %v, want %v", k, got[k], w)
		}
	}
}

func TestReadBootInstallStatusFollowsTheEntrypointsEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "install.tsv")
	if err := os.WriteFile(p, []byte("claude\tfailed\tno_space\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := bootInstallStatusPath
	bootInstallStatusPath = ""
	t.Cleanup(func() { bootInstallStatusPath = old })

	t.Setenv("AF_BOOT_INSTALL_STATUS", "")
	if got := readBootInstallStatus(); got != nil {
		t.Fatalf("no env means the entrypoint recorded nothing; an old file must not be read, got %v", got)
	}
	t.Setenv("AF_BOOT_INSTALL_STATUS", p)
	if got := readBootInstallStatus(); got["claude"].Reason != "no_space" {
		t.Fatalf("with the env set the file is read, got %v", got)
	}

	// A directory or file others can write is not private: refused.
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if got := readBootInstallStatus(); got != nil {
		t.Errorf("a world-writable directory must read as unknown, got %v", got)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o666); err != nil {
		t.Fatal(err)
	}
	if got := readBootInstallStatus(); got != nil {
		t.Errorf("a world-writable file must read as unknown, got %v", got)
	}
}

func TestReadBootInstallStatusMissingIsUnknown(t *testing.T) {
	old := bootInstallStatusPath
	bootInstallStatusPath = filepath.Join(t.TempDir(), "absent")
	t.Cleanup(func() { bootInstallStatusPath = old })
	if got := readBootInstallStatus(); got != nil {
		t.Fatalf("a missing file must read as unknown (nil), got %v", got)
	}
}

func TestReadBootInstallStatusRefusesWhatTheEntrypointDidNotWrite(t *testing.T) {
	dir := t.TempDir()
	good := "claude\tfailed\tno_space\n"
	old := bootInstallStatusPath
	t.Cleanup(func() { bootInstallStatusPath = old })

	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	bootInstallStatusPath = write("ok.tsv", []byte(good))
	if got := readBootInstallStatus(); got["claude"].Reason != "no_space" {
		t.Fatalf("baseline: %v", got)
	}

	// Larger than any real file: unknown, not truncated-and-parsed.
	big := []byte(good)
	for len(big) <= bootInstallMaxBytes {
		big = append(big, []byte("codex\tok\t\n")...)
	}
	bootInstallStatusPath = write("big.tsv", big)
	if got := readBootInstallStatus(); got != nil {
		t.Errorf("an oversized file must read as unknown, got %d entries", len(got))
	}

	// A symlink to a fine file is not followed.
	target := write("target.tsv", []byte(good))
	link := filepath.Join(dir, "link.tsv")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	bootInstallStatusPath = link
	if got := readBootInstallStatus(); got != nil {
		t.Errorf("a symlink must read as unknown, got %v", got)
	}

	// A FIFO must neither block the reader nor be read.
	fifo := filepath.Join(dir, "fifo.tsv")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("no FIFOs here:", err)
	}
	bootInstallStatusPath = fifo
	done := make(chan map[string]bootInstall, 1)
	go func() { done <- readBootInstallStatus() }()
	select {
	case got := <-done:
		if got != nil {
			t.Errorf("a FIFO must read as unknown, got %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}

	// A directory that is itself a symlink is not trusted either.
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "install.tsv"), []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(dir, "linkdir")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	bootInstallStatusPath = filepath.Join(linkDir, "install.tsv")
	if got := readBootInstallStatus(); got != nil {
		t.Errorf("a symlinked directory must read as unknown, got %v", got)
	}
}
