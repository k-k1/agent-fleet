package main

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// isolateCLIVersions points home, /proc, the npm roots and PATH lookup at temp dirs, so a
// copilot or cursor installed on the machine running the test is never read or removed.
func isolateCLIVersions(t *testing.T) (home, proc string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	proc = t.TempDir()
	oldProc, oldRoots, oldLook := procRoot, npmGlobalRoots, lookPathFn
	procRoot = proc
	npmGlobalRoots = func(h string) []string { return []string{filepath.Join(h, ".local/lib/node_modules")} }
	lookPathFn = func(string) (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { procRoot, npmGlobalRoots, lookPathFn = oldProc, oldRoots, oldLook })
	return home, proc
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(link)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func cursorRoot(home string) string  { return filepath.Join(home, ".local/share/cursor-agent/versions") }
func copilotRoot(home string) string { return filepath.Join(home, ".cache/copilot/pkg/linux-x64") }

// installCursor unpacks a fake cursor version and points both launchers at it, the way
// entrypoint.sh and upstream install.sh do.
func installCursor(t *testing.T, home, ver string) {
	t.Helper()
	dir := filepath.Join(cursorRoot(home), ver)
	writeSized(t, filepath.Join(dir, "node"), 1024)
	writeSized(t, filepath.Join(dir, "cursor-agent"), 16)
	mustSymlink(t, filepath.Join(dir, "cursor-agent"), filepath.Join(home, ".local/bin/cursor-agent"))
	mustSymlink(t, filepath.Join(dir, "cursor-agent"), filepath.Join(home, ".local/bin/agent"))
}

// installCopilot npm-installs a fake copilot version and runs it once, which extracts its
// package under ~/.cache/copilot/pkg.
func installCopilot(t *testing.T, home, ver string) {
	t.Helper()
	pkg := filepath.Join(home, ".local/lib/node_modules/@github/copilot")
	writeSized(t, filepath.Join(pkg, "npm-loader.js"), 8)
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@github/copilot","version":"`+ver+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	plat := filepath.Join(pkg, "node_modules/@github/copilot-linux-x64")
	writeSized(t, filepath.Join(plat, "copilot"), 8)
	if err := os.WriteFile(filepath.Join(plat, "package.json"), []byte(`{"version":"`+ver+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSized(t, filepath.Join(copilotRoot(home), ver, "app.js"), 2048)
}

// fakeProcFiles adds a process with the given executable, mapped files and open files.
func fakeProcFiles(t *testing.T, proc, pid, exe string, maps []string, fds []string) {
	t.Helper()
	dir := filepath.Join(proc, pid)
	if err := os.MkdirAll(filepath.Join(dir, "fd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if exe != "" {
		mustSymlink(t, exe, filepath.Join(dir, "exe"))
	}
	var m string
	for _, p := range maps {
		m += "7f0000000000-7f0000001000 r-xp 00000000 08:01 1234    " + p + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "maps"), []byte(m), 0o644); err != nil {
		t.Fatal(err)
	}
	for i, p := range fds {
		mustSymlink(t, p, filepath.Join(dir, "fd", string(rune('3'+i))))
	}
}

func versionsLeft(t *testing.T, root string) []string {
	t.Helper()
	ents, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func assertVersions(t *testing.T, root string, want ...string) {
	t.Helper()
	got := versionsLeft(t, root)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", root, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: got %v, want %v", root, got, want)
		}
	}
}

// The acceptance case: after two pin bumps (plus an opt-in update past the pin) only the
// current and pinned versions remain.
func TestPruneKeepsOnlyCurrentAndPinnedAfterTwoBumps(t *testing.T) {
	home, _ := isolateCLIVersions(t)
	for _, v := range []string{"2026.07.20-8cc9c0b", "2026.08.04-aaa8809", "2026.09.15-d2fe57e", "2026.09.23-86fc751"} {
		installCursor(t, home, v)
	}
	for _, v := range []string{"1.0.73", "1.0.80", "1.0.87", "1.0.88"} {
		installCopilot(t, home, v)
	}
	pins := map[string]string{"cursor": "2026.09.15-d2fe57e", "copilot": "1.0.87"}

	got := pruneOldCLIVersions(home, pins)

	assertVersions(t, cursorRoot(home), "2026.09.15-d2fe57e", "2026.09.23-86fc751")
	assertVersions(t, copilotRoot(home), "1.0.87", "1.0.88")
	if len(got) != 4 {
		t.Fatalf("pruned %+v, want 4 entries", got)
	}
	for _, p := range got {
		if p.Bytes == 0 {
			t.Errorf("%s %s: Bytes = 0, want the size that was freed", p.CLI, p.Version)
		}
	}
	// A second boot has nothing left to do.
	if again := pruneOldCLIVersions(home, pins); len(again) != 0 {
		t.Fatalf("second pass pruned %+v", again)
	}
}

// A session still running an old version keeps it, whichever way /proc shows the use.
func TestPruneKeepsVersionsInUse(t *testing.T) {
	home, proc := isolateCLIVersions(t)
	for _, v := range []string{"2026.07.20-8cc9c0b", "2026.08.04-aaa8809", "2026.09.23-86fc751"} {
		installCursor(t, home, v)
	}
	for _, v := range []string{"1.0.73", "1.0.80", "1.0.81", "1.0.88"} {
		installCopilot(t, home, v)
	}
	// cursor's bundled node is the executable.
	fakeProcFiles(t, proc, "100", filepath.Join(cursorRoot(home), "2026.07.20-8cc9c0b/node"), nil, nil)
	// A copilot whose extracted package was since replaced on disk: only its maps and open
	// files still name the version.
	exe := filepath.Join(home, ".local/lib/node_modules/@github/copilot/node_modules/@github/copilot-linux-x64/copilot")
	fakeProcFiles(t, proc, "200", exe,
		[]string{filepath.Join(copilotRoot(home), "1.0.73/prebuilds/linux-x64/runtime.node") + " (deleted)"},
		[]string{filepath.Join(copilotRoot(home), "1.0.80/app.js")})
	// Something unrelated.
	fakeProcFiles(t, proc, "300", "/usr/bin/bash", []string{"/usr/lib/libc.so.6"}, nil)

	pruneOldCLIVersions(home, map[string]string{})

	assertVersions(t, cursorRoot(home), "2026.07.20-8cc9c0b", "2026.09.23-86fc751")
	assertVersions(t, copilotRoot(home), "1.0.73", "1.0.80", "1.0.88")
}

// A running copilot whose binary was replaced cannot say which package it extracted, so
// no copilot version goes; cursor is unaffected.
func TestPruneSkipsCopilotRunningAnUnknownVersion(t *testing.T) {
	home, proc := isolateCLIVersions(t)
	installCopilot(t, home, "1.0.73")
	installCopilot(t, home, "1.0.88")
	installCursor(t, home, "2026.07.20-8cc9c0b")
	installCursor(t, home, "2026.09.23-86fc751")
	exe := filepath.Join(home, ".local/lib/node_modules/@github/copilot/node_modules/@github/copilot-linux-x64/copilot")
	fakeProcFiles(t, proc, "200", exe+" (deleted)", nil, nil)

	pruneOldCLIVersions(home, map[string]string{})

	assertVersions(t, copilotRoot(home), "1.0.73", "1.0.88")
	assertVersions(t, cursorRoot(home), "2026.09.23-86fc751")
}

// When the current version cannot be told, nothing of that CLI goes.
func TestPruneKeepsEverythingWhenCurrentIsUnknown(t *testing.T) {
	home, _ := isolateCLIVersions(t)
	installCursor(t, home, "2026.07.20-8cc9c0b")
	installCursor(t, home, "2026.09.23-86fc751")
	// The launcher now points outside home (a baked /usr/local install, say).
	mustSymlink(t, "/usr/local/share/cursor-agent/versions/2026.09.15-d2fe57e/cursor-agent",
		filepath.Join(home, ".local/bin/cursor-agent"))
	installCopilot(t, home, "1.0.73")
	installCopilot(t, home, "1.0.88")
	if err := os.RemoveAll(filepath.Join(home, ".local/lib/node_modules/@github/copilot")); err != nil {
		t.Fatal(err)
	}

	if got := pruneOldCLIVersions(home, map[string]string{}); len(got) != 0 {
		t.Fatalf("pruned %+v, want nothing", got)
	}
	assertVersions(t, cursorRoot(home), "2026.07.20-8cc9c0b", "2026.09.23-86fc751")
	assertVersions(t, copilotRoot(home), "1.0.73", "1.0.88")
}

// Only directories named like a version go: not stray files, not other directories, and
// never through a symlink.
func TestPruneTouchesOnlyVersionDirectories(t *testing.T) {
	home, _ := isolateCLIVersions(t)
	installCursor(t, home, "2026.09.23-86fc751")
	root := cursorRoot(home)
	writeSized(t, filepath.Join(root, "notes/keep.txt"), 4)
	writeSized(t, filepath.Join(root, "2026.08.04-aaa8809.tgz"), 4)
	elsewhere := filepath.Join(t.TempDir(), "2026.07.20-8cc9c0b")
	writeSized(t, filepath.Join(elsewhere, "node"), 4)
	mustSymlink(t, elsewhere, filepath.Join(root, "2026.07.20-8cc9c0b"))

	if got := pruneOldCLIVersions(home, map[string]string{}); len(got) != 0 {
		t.Fatalf("pruned %+v, want nothing", got)
	}
	assertVersions(t, root, "2026.07.20-8cc9c0b", "2026.08.04-aaa8809.tgz", "2026.09.23-86fc751", "notes")
	if _, err := os.Stat(filepath.Join(elsewhere, "node")); err != nil {
		t.Fatalf("symlink target touched: %v", err)
	}
}

// Read-only directories inside a version (a tool may leave them) do not stop the removal.
func TestPruneRemovesReadOnlyTrees(t *testing.T) {
	home, _ := isolateCLIVersions(t)
	installCursor(t, home, "2026.07.20-8cc9c0b")
	installCursor(t, home, "2026.09.23-86fc751")
	ro := filepath.Join(cursorRoot(home), "2026.07.20-8cc9c0b/lib")
	writeSized(t, filepath.Join(ro, "x.node"), 4)
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })

	pruneOldCLIVersions(home, map[string]string{})

	assertVersions(t, cursorRoot(home), "2026.09.23-86fc751")
}

// Outside a Workspace image there is no versions.json, and home belongs to whoever runs
// the Agent (the native runtime): nothing is removed.
func TestPruneAtBootNeedsVersionsJSON(t *testing.T) {
	home, _ := isolateCLIVersions(t)
	installCursor(t, home, "2026.07.20-8cc9c0b")
	installCursor(t, home, "2026.09.23-86fc751")
	old := buildPinsPath
	buildPinsPath = filepath.Join(t.TempDir(), "missing.json")
	t.Cleanup(func() { buildPinsPath = old })

	pruneOldCLIVersionsAtBoot()

	assertVersions(t, cursorRoot(home), "2026.07.20-8cc9c0b", "2026.09.23-86fc751")
}
