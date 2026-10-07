package mdblock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const (
	gs = "<!-- agent-fleet:memory-guide -->"
	ge = "<!-- /agent-fleet:memory-guide -->"
)

// Damaged markers are refused and the input comes back unchanged, whether on or off.
func TestSetSafeRefusesDamagedMarkersAndKeepsEveryByte(t *testing.T) {
	for name, in := range map[string]string{
		"missing end":      "USER BEFORE\n" + gs + "\nold\nUSER AFTER\n",
		"stray end":        "USER BEFORE\n" + ge + "\nUSER AFTER\n",
		"nested start":     gs + "\na\n" + gs + "\nb\n" + ge + "\nUSER AFTER\n",
		"end then more":    gs + "\na\n" + ge + "\nX\n" + ge + "\nUSER AFTER\n",
		"missing end crlf": "USER BEFORE\r\n" + gs + "\r\nold\r\nUSER AFTER\r\n",
	} {
		for _, body := range []string{"", "new"} {
			out, err := SetSafe(in, "memory-guide", body)
			if !errors.Is(err, ErrDamaged) || out != in {
				t.Errorf("%s (body %q): err=%v, input changed=%v", name, body, err, out != in)
			}
		}
	}
}

// Well-formed copies, however many, all go on "off"; the member's text stays.
func TestSetSafeRemovesEveryWellFormedCopyAndKeepsUserText(t *testing.T) {
	in := "USER BEFORE\n\n" + gs + "\na\n" + ge + "\n\nMIDDLE\n\n" + gs + "\nb\n" + ge + "\n\nUSER AFTER\n"
	out, err := SetSafe(in, "memory-guide", "")
	if err != nil || Has(out, "memory-guide") {
		t.Fatalf("err=%v out=%q", err, out)
	}
	for _, w := range []string{"USER BEFORE", "MIDDLE", "USER AFTER"} {
		if !contains(out, w) {
			t.Errorf("%q lost: %q", w, out)
		}
	}
	on, err := SetSafe(in, "memory-guide", "new")
	if err != nil || countMarkers(on, "memory-guide") != 1 {
		t.Fatalf("on must leave exactly one block: err=%v %q", err, on)
	}
}

// CRLF files with well-formed blocks keep their text and lose the block.
func TestSetSafeHandlesCRLF(t *testing.T) {
	in := "USER BEFORE\r\n\r\n" + gs + "\r\nold\r\n" + ge + "\r\n\r\nUSER AFTER\r\n"
	out, err := SetSafe(in, "memory-guide", "")
	if err != nil || Has(out, "memory-guide") || !contains(out, "USER BEFORE") || !contains(out, "USER AFTER") {
		t.Fatalf("err=%v out=%q", err, out)
	}
}

// A symlink is written through: the link survives, the target changes, and its mode is kept.
func TestEditFileWritesThroughASymlinkKeepingModeAndLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "store", "real.md")
	link := filepath.Join(dir, "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("MINE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	add := func(s string) (string, error) { return SetSafe(s, "memory-guide", "guide") }
	if err := EditFile(link, 0o644, 0o755, true, add); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link was replaced by a regular file")
	}
	b, _ := os.ReadFile(target)
	if !Has(string(b), "memory-guide") || !contains(string(b), "MINE") {
		t.Fatalf("target: %q", b)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode().Perm())
	}
	// Off on a link whose target holds only the block empties the target, never deletes the link.
	if err := os.WriteFile(target, []byte(gs+"\nguide\n"+ge+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EditFile(link, 0o644, 0o755, true, func(s string) (string, error) { return SetSafe(s, "memory-guide", "") }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("link gone: %v", err)
	}
	if b, _ := os.ReadFile(target); Has(string(b), "memory-guide") {
		t.Fatalf("guide left in the target: %q", b)
	}
}

func TestEditFileRefusesADanglingLink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "AGENTS.md")
	if err := os.Symlink(filepath.Join(dir, "nowhere"), link); err != nil {
		t.Fatal(err)
	}
	if err := EditFile(link, 0o644, 0o755, true, func(s string) (string, error) { return "x", nil }); err == nil {
		t.Fatal("a dangling link must not be replaced")
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the dangling link was replaced")
	}
}
