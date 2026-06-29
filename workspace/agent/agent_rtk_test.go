package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyCodexRTKToggle(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	path := filepath.Join(dir, "AGENTS.md")
	base := "# Workspace Guide\n\nbase line one\nbase line two\n"
	if err := os.WriteFile(path, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}

	// on: appends a marked block, base preserved.
	applyCodexRTK(true)
	b, _ := os.ReadFile(path)
	got := string(b)
	if !strings.Contains(got, codexRTKMarkerStart) || !strings.Contains(got, codexRTKMarkerEnd) {
		t.Fatalf("markers missing after on:\n%s", got)
	}
	if !strings.HasPrefix(got, base) {
		t.Fatalf("base content not preserved:\n%s", got)
	}
	if strings.Count(got, "rtk (token saver)") != 1 {
		t.Fatalf("expected exactly one block:\n%s", got)
	}

	// on again: idempotent, no duplicate block.
	applyCodexRTK(true)
	b, _ = os.ReadFile(path)
	if c := strings.Count(string(b), codexRTKMarkerStart); c != 1 {
		t.Fatalf("expected 1 start marker after re-apply, got %d", c)
	}

	// off: block removed, base restored exactly.
	applyCodexRTK(false)
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), codexRTKMarkerStart) {
		t.Fatalf("block not removed:\n%s", string(b))
	}
	if string(b) != base {
		t.Fatalf("base not restored exactly:\n%q\nwant:\n%q", string(b), base)
	}

	// off again on a file with no block: unchanged (no spurious write content).
	applyCodexRTK(false)
	b, _ = os.ReadFile(path)
	if string(b) != base {
		t.Fatalf("off-idempotent failed:\n%q", string(b))
	}
}

func TestApplyCodexRTKNoBaseFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	path := filepath.Join(dir, "AGENTS.md")

	// off with no file: nothing created.
	applyCodexRTK(false)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("off created a file when none existed")
	}

	// on with no base file: creates file with just the block.
	applyCodexRTK(true)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("on did not create file: %v", err)
	}
	if !strings.Contains(string(b), codexRTKMarkerStart) {
		t.Fatalf("block missing:\n%s", string(b))
	}
}

func TestStripMarkedBlock(t *testing.T) {
	s := "head\n\n" + codexRTKMarkerStart + "\nblock\n" + codexRTKMarkerEnd + "\n\ntail\n"
	got := stripMarkedBlock(s, codexRTKMarkerStart, codexRTKMarkerEnd)
	if strings.Contains(got, "block") || strings.Contains(got, codexRTKMarkerStart) {
		t.Fatalf("strip left residue: %q", got)
	}
	if !strings.Contains(got, "head") || !strings.Contains(got, "tail") {
		t.Fatalf("strip ate surrounding content: %q", got)
	}
	// no markers: unchanged.
	if stripMarkedBlock("plain text", codexRTKMarkerStart, codexRTKMarkerEnd) != "plain text" {
		t.Fatal("strip changed marker-free text")
	}
}

func TestAgentRTKPrefsDefaultOn(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// absent file → both default on.
	p := readAgentRTKPrefs()
	if !prefOnDefault(p.Codex) || !prefOnDefault(p.Opencode) {
		t.Fatal("absent prefs should default to on")
	}
	// explicit false persists and reads back false.
	no := false
	if err := writeAgentRTKPrefs(agentRTKPrefs{Codex: &no}); err != nil {
		t.Fatal(err)
	}
	p = readAgentRTKPrefs()
	if prefOnDefault(p.Codex) {
		t.Fatal("explicit codex=false should read back false")
	}
	if !prefOnDefault(p.Opencode) {
		t.Fatal("unset opencode should still default on")
	}
}
