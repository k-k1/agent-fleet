package session

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCleanSubdir pins the normalization + rejection rules the create handler relies on:
// the field means "a folder beneath the working copy", so decoration is trimmed and any
// escape (absolute, "..", "~") is refused rather than silently clamped.
func TestCleanSubdir(t *testing.T) {
	ok := map[string]string{
		"":                "",
		"   ":             "",
		"console":         "console",
		"./console/src":   "console/src",
		"console//src":    "console/src",
		`console\src`:     "console/src", // a Windows-style paste
		"a/./b":           "a/b",
		"apps/web/../web": "apps/web", // resolves back inside — allowed
	}
	for in, want := range ok {
		got, valid := CleanSubdir(in)
		if !valid || got != want {
			t.Errorf("CleanSubdir(%q) = %q,%v; want %q,true", in, got, valid, want)
		}
	}
	// Escapes and pasted absolute paths are refused outright — never clamped into
	// something plausible, which would launch the session in the wrong folder.
	for _, in := range []string{"/", "..", "../sibling", "console/../..", "~/repos/other", "/abs/path", "/console"} {
		if _, valid := CleanSubdir(in); valid {
			t.Errorf("CleanSubdir(%q) accepted; want rejected", in)
		}
	}
}

// TestMetaCWD covers where the agent process actually starts: Dir when no subdir is
// recorded, the subdir when it exists, and — deliberately — Dir again when the recorded
// subdir has since disappeared, so a deleted folder can never make a session unstartable.
func TestMetaCWD(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "console", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := (Meta{Dir: root}).CWD(); got != root {
		t.Errorf("no subdir: CWD = %q, want %q", got, root)
	}
	want := filepath.Join(root, "console", "src")
	if got := (Meta{Dir: root, Subdir: "console/src"}).CWD(); got != want {
		t.Errorf("subdir: CWD = %q, want %q", got, want)
	}
	if got := (Meta{Dir: root, Subdir: "gone/away"}).CWD(); got != root {
		t.Errorf("missing subdir: CWD = %q, want fallback %q", got, root)
	}
}
