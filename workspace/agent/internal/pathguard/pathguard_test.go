package pathguard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveLexical(t *testing.T) {
	root := "/x/root"
	cases := []struct {
		rel     string
		wantOK  bool
		wantRel string
	}{
		{"a/b.txt", true, "a/b.txt"},
		{".", true, ""},
		{"", true, ""},
		{"..", false, ""},
		{"../etc/passwd", false, ""},
		{"a/../../etc/passwd", false, ""},
		{"a/../b", true, "b"},
	}
	for _, c := range cases {
		full, rel, ok := Resolve(root, c.rel)
		if ok != c.wantOK {
			t.Errorf("Resolve(%q,%q) ok=%v want %v", root, c.rel, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if filepath.ToSlash(rel) != c.wantRel {
			t.Errorf("Resolve(%q,%q) rel=%q want %q", root, c.rel, rel, c.wantRel)
		}
		if !filepath.IsAbs(full) {
			t.Errorf("Resolve(%q,%q) full=%q not absolute", root, c.rel, full)
		}
	}
}

func TestResolveUnderRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("shh"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink INSIDE root pointing OUTSIDE it — the case a lexical-only check
	// (Resolve) cannot catch, since "link/secret.txt" never contains "..".
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	full := filepath.Join(link, "secret.txt")

	if _, ok := ResolveUnder(full, root); ok {
		t.Fatalf("ResolveUnder allowed a symlink escape: %s -> %s", full, outside)
	}

	// Positive control: without the EvalSymlinks re-check, a naive "does the
	// lexical path start with root" test WOULD accept this same path. Assert that
	// belief explicitly, so a future refactor that regresses ResolveUnder back to
	// a lexical-only check is caught by this same test file.
	lexicalFull, _, lexOK := Resolve(root, "link/secret.txt")
	if !lexOK || lexicalFull != full {
		t.Fatalf("test setup: expected lexical Resolve to accept %q", full)
	}
}

func TestResolveUnderAllowsNotYetExistingWriteTarget(t *testing.T) {
	root := t.TempDir()
	full := filepath.Join(root, "new", "file.txt")
	rel, ok := ResolveUnder(full, root)
	if !ok {
		t.Fatalf("ResolveUnder rejected a not-yet-existing path under root: %s", full)
	}
	if filepath.ToSlash(rel) != "new/file.txt" {
		t.Fatalf("rel = %q, want new/file.txt", rel)
	}
}

func TestResolveUnderRejectsDanglingSymlink(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "does-not-exist"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, ok := ResolveUnder(link, root); ok {
		t.Fatalf("ResolveUnder accepted a dangling symlink: %s", link)
	}
}
