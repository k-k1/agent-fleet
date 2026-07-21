package main

import (
	"os"
	"path/filepath"
	"testing"
)

// goRootFor resolves the on-demand GOROOT under the home share dir; ""/"system"
// mean "no override" (baked or nothing), and an uninstalled version resolves
// empty rather than pointing at a dir with no toolchain in it.
func TestGoRootFor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	goBin := filepath.Join(home, ".local/share/agent-fleet/go/1.26.4/bin/go")
	if err := os.MkdirAll(filepath.Dir(goBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := goRootFor("1.26.4"); got != filepath.Join(home, ".local/share/agent-fleet/go/1.26.4") {
		t.Errorf("goRootFor(1.26.4) = %q", got)
	}
	for _, sel := range []string{"", "system", "9.9.9"} {
		if got := goRootFor(sel); got != "" {
			t.Errorf("goRootFor(%q) = %q, want empty", sel, got)
		}
	}
	if got := installedGoVersions(); len(got) != 1 || got[0] != "1.26.4" {
		t.Errorf("installedGoVersions = %v", got)
	}
}

// With no chromium pin (dev host has no versions.json), the newest installed
// on-demand build wins; nothing installed resolves empty. Both the legacy
// chrome-linux and the Chrome for Testing chrome-linux64 layouts resolve.
func TestChromiumPinnedBinary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := chromiumPinnedBinary(); got != "" {
		t.Fatalf("empty install: got %q", got)
	}
	for pin, sub := range map[string]string{"1200": "chrome-linux", "149.0.7827.55": "chrome-linux64"} {
		p := filepath.Join(home, ".local/share/agent-fleet/chromium", pin, sub, "chrome")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// lexicographic glob: "149.0.7827.55" > "1200" — the CfT dir wins as newest
	want := filepath.Join(home, ".local/share/agent-fleet/chromium/149.0.7827.55/chrome-linux64/chrome")
	if got := chromiumPinnedBinary(); got != want {
		t.Errorf("chromiumPinnedBinary = %q, want %q (newest build)", got, want)
	}
}

func TestVerifySha256(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// sha256sum of "hello\n"
	want := "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
	if err := verifySha256(p, want); err != nil {
		t.Errorf("verifySha256 (match): %v", err)
	}
	if err := verifySha256(p, "deadbeef"); err == nil {
		t.Error("verifySha256 (mismatch): expected error")
	}
}

// The go selector offers "system" plus installed on-demand versions (build pin
// first when present — absent on a dev host without versions.json).
func TestGoOptions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	goBin := filepath.Join(home, ".local/share/agent-fleet/go/1.25.0/bin/go")
	if err := os.MkdirAll(filepath.Dir(goBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := goOptions()
	if len(got) < 2 || got[0] != "system" {
		t.Fatalf("goOptions = %v, want [system ... 1.25.0]", got)
	}
	found := false
	for _, v := range got {
		if v == "1.25.0" {
			found = true
		}
	}
	if !found {
		t.Errorf("goOptions = %v missing installed 1.25.0", got)
	}
}
