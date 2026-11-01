package opencode

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeInstall lays out an npm global tree for pkg@version and returns the PATH symlink to its
// binary, the shape `npm install -g` leaves behind.
func fakeInstall(t *testing.T, pkg, version string) string {
	t.Helper()
	root := t.TempDir()
	pkgDir := filepath.Join(root, "lib", "node_modules", filepath.FromSlash(pkg))
	if err := os.MkdirAll(filepath.Join(pkgDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"` + pkg + `","version":"` + version + `"}`
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(pkgDir, "bin", "opencode.exe")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "bin", "opencode")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	return link
}

// useOpencode points the major detection at a fake install and an absent pin list.
func useOpencode(t *testing.T, bin string) {
	t.Helper()
	oldBin, oldPins := opencodeBinary, buildPinsPath
	opencodeBinary = func() string { return bin }
	buildPinsPath = filepath.Join(t.TempDir(), "versions.json")
	t.Cleanup(func() { opencodeBinary, buildPinsPath = oldBin, oldPins })
}

// useMajor installs a fake opencode of the given major for the duration of the test.
func useMajor(t *testing.T, major int) {
	t.Helper()
	if major == 2 {
		useOpencode(t, fakeInstall(t, "@opencode/cli", "2.0.28"))
		return
	}
	useOpencode(t, fakeInstall(t, "opencode-ai", "1.19.0"))
}

func TestInstalledMajorFromPackage(t *testing.T) {
	useOpencode(t, fakeInstall(t, "@opencode/cli", "2.0.28"))
	if got := installedMajor(); got != 2 {
		t.Fatalf("@opencode/cli 2.0.28: major = %d, want 2", got)
	}
	useOpencode(t, fakeInstall(t, "opencode-ai", "1.19.0"))
	if got := installedMajor(); got != 1 {
		t.Fatalf("opencode-ai 1.19.0: major = %d, want 1", got)
	}
}

func TestInstalledMajorFallsBackToPinThenUnknown(t *testing.T) {
	// A binary with no manifest beside it (copied or wrapped): the image pin decides.
	bin := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	useOpencode(t, bin)
	if got := installedMajor(); got != 0 {
		t.Fatalf("no manifest, no pin: major = %d, want 0 (unknown)", got)
	}
	if err := os.WriteFile(buildPinsPath, []byte(`{"opencode":"2.0.28"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := installedMajor(); got != 2 {
		t.Fatalf("pin 2.0.28: major = %d, want 2", got)
	}
	// A manifest of some other package is not evidence.
	other := fakeInstall(t, "left-pad", "9.0.0")
	useOpencode(t, other)
	if got := installedMajor(); got != 0 {
		t.Fatalf("foreign manifest: major = %d, want 0", got)
	}
}
