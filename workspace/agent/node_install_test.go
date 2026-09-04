package main

import (
	"os"
	"path/filepath"
	"testing"
)

// installedNodeMajors feeds the picker's "installed vs installable" distinction — the
// thing whose absence let a member select a version that was never going to arrive.
func TestInstalledNodeMajors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".nvm", "versions", "node")
	for _, v := range []string{"v22.23.2", "v22.9.0", "v20.1.10", "v18.20.4"} {
		if err := os.MkdirAll(filepath.Join(root, v), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// nvm also keeps non-version entries; they must not turn into a "major".
	if err := os.MkdirAll(filepath.Join(root, "alias"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := installedNodeMajors()
	want := map[string]bool{"22": true, "20": true, "18": true}
	if len(got) != len(want) {
		t.Fatalf("installedNodeMajors() = %v, want the 3 majors only", got)
	}
	for _, m := range got {
		if !want[m] {
			t.Errorf("unexpected major %q in %v", m, got)
		}
	}
}

func TestInstalledNodeMajorsEmptyWhenNoNvm(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := installedNodeMajors(); len(got) != 0 {
		t.Fatalf("installedNodeMajors() = %v, want empty", got)
	}
}

// The arch that reaches the download URL and the tarball name.
func TestNodeDistArch(t *testing.T) {
	if a := nodeDistArch(); a != "x64" && a != "arm64" {
		t.Fatalf("nodeDistArch() = %q, want x64 or arm64", a)
	}
}
