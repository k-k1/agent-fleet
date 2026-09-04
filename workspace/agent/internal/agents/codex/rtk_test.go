package codex

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
	ApplyRTK(true)
	b, _ := os.ReadFile(path)
	got := string(b)
	if !strings.Contains(got, rtkMarkerStart) || !strings.Contains(got, rtkMarkerEnd) {
		t.Fatalf("markers missing after on:\n%s", got)
	}
	if !strings.HasPrefix(got, base) {
		t.Fatalf("base content not preserved:\n%s", got)
	}
	if strings.Count(got, "rtk (token saver)") != 1 {
		t.Fatalf("expected exactly one block:\n%s", got)
	}

	// on again: idempotent, no duplicate block.
	ApplyRTK(true)
	b, _ = os.ReadFile(path)
	if c := strings.Count(string(b), rtkMarkerStart); c != 1 {
		t.Fatalf("expected 1 start marker after re-apply, got %d", c)
	}

	// off: block removed, base restored exactly.
	ApplyRTK(false)
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), rtkMarkerStart) {
		t.Fatalf("block not removed:\n%s", string(b))
	}
	if string(b) != base {
		t.Fatalf("base not restored exactly:\n%q\nwant:\n%q", string(b), base)
	}

	// off again on a file with no block: unchanged (no spurious write content).
	ApplyRTK(false)
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
	ApplyRTK(false)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("off created a file when none existed")
	}

	// on with no base file: creates file with just the block.
	ApplyRTK(true)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("on did not create file: %v", err)
	}
	if !strings.Contains(string(b), rtkMarkerStart) {
		t.Fatalf("block missing:\n%s", string(b))
	}
}

// Stripping/appending the marker itself is unit-tested in internal/mdblock, which holds the
// shared implementation for codex / agy / user instructions. Here only the AGENTS.md
// application side is covered.
