package fleetskills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const topic = "---\nname: af-environment\ndescription: \"read before x\"\nuser-invocable: false\n---\n# Environment\n\nbody\n"

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadTakesOnlyValidStems(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "environment.md"), topic)
	write(t, filepath.Join(dir, "Bad Name.md"), "x")
	write(t, filepath.Join(dir, "readme.txt"), "x")
	write(t, filepath.Join(dir, "sub", "nested.md"), "x")
	got := Load(dir)
	if len(got) != 1 || got["environment"] != topic {
		t.Fatalf("Load = %v", got)
	}
	if Load(filepath.Join(dir, "missing")) != nil {
		t.Fatal("missing dir must yield nil")
	}
}

func TestApplyWritesMarkerAfterFrontmatterAndIsIdempotent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	if err := Apply(root, map[string]string{"environment": topic}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "af-environment", "SKILL.md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	// The frontmatter must be byte-identical to the source (the CLIs parse it), and the
	// marker must be the first body line.
	head := "---\nname: af-environment\ndescription: \"read before x\"\nuser-invocable: false\n---\n"
	if !strings.HasPrefix(got, head+Marker+"\n# Environment") {
		t.Fatalf("unexpected layout:\n%s", got)
	}
	st1, _ := os.Stat(path)
	if err := Apply(root, map[string]string{"environment": topic}); err != nil {
		t.Fatal(err)
	}
	st2, _ := os.Stat(path)
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatal("unchanged content was rewritten")
	}
	// No frontmatter: marker goes on top.
	if err := Apply(root, map[string]string{"plain": "# Plain\n"}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(root, "af-plain", "SKILL.md"))
	if !strings.HasPrefix(string(b), Marker+"\n\n# Plain") {
		t.Fatalf("plain layout:\n%s", b)
	}
}

func TestApplyPrunesOnlyOwnedDirectories(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "af-old", "SKILL.md"), withMarker("---\nname: af-old\n---\nold\n"))
	write(t, filepath.Join(root, "af-mine", "SKILL.md"), "---\nname: af-mine\n---\nthe user's own\n")
	write(t, filepath.Join(root, "other", "SKILL.md"), "---\nname: other\n---\nx\n")
	if err := Apply(root, map[string]string{"environment": topic}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "af-old")); !os.IsNotExist(err) {
		t.Fatal("stale AF-owned skill must be removed")
	}
	for _, keep := range []string{"af-mine", "other", "af-environment"} {
		if _, err := os.Stat(filepath.Join(root, keep, "SKILL.md")); err != nil {
			t.Fatalf("%s must survive: %v", keep, err)
		}
	}
	// A user's `af-<stem>` that collides with a topic is neither overwritten nor deleted.
	if err := Apply(root, map[string]string{"mine": topic}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "af-mine", "SKILL.md"))
	if !strings.Contains(string(b), "the user's own") {
		t.Fatal("user-owned af-mine was overwritten")
	}
}

func TestApplyEmptyIsNoop(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	write(t, filepath.Join(root, "af-old", "SKILL.md"), withMarker("old\n"))
	if err := Apply(root, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "af-old", "SKILL.md")); err != nil {
		t.Fatal("empty topic set must not remove anything")
	}
	absent := filepath.Join(t.TempDir(), "never")
	if err := Apply(absent, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Fatal("empty topic set must not create the root")
	}
}

func TestApplyLeavesUnownedDirectoriesAndLinksAlone(t *testing.T) {
	for _, kind := range []string{"missing-skill", "directory-link", "file-link", "quoted-marker"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "af-environment")
			external := t.TempDir()
			write(t, filepath.Join(external, "SKILL.md"), withMarker("external"))
			if kind == "directory-link" {
				if err := os.Symlink(external, dir); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if kind == "file-link" {
					if err := os.Symlink(filepath.Join(external, "SKILL.md"), filepath.Join(dir, "SKILL.md")); err != nil {
						t.Fatal(err)
					}
				} else if kind == "quoted-marker" {
					write(t, filepath.Join(dir, "SKILL.md"), "User documentation\n```\n"+Marker+"\n```\n")
				}
			}
			if err := Apply(root, map[string]string{"environment": topic}); err != nil {
				t.Fatal(err)
			}
			if kind == "missing-skill" {
				if _, err := os.Lstat(filepath.Join(dir, "SKILL.md")); !os.IsNotExist(err) {
					t.Fatal("claimed user directory")
				}
			} else if kind == "file-link" {
				if st, err := os.Lstat(filepath.Join(dir, "SKILL.md")); err != nil || st.Mode()&os.ModeSymlink == 0 {
					t.Fatal("replaced user link")
				}
			} else if kind == "quoted-marker" {
				b, _ := os.ReadFile(filepath.Join(dir, "SKILL.md"))
				if !strings.HasPrefix(string(b), "User documentation") {
					t.Fatal("overwrote quoted marker")
				}
			}
			if err := Apply(root, map[string]string{"other": topic}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(dir); err != nil {
				t.Fatalf("removed user directory: %v", err)
			}
			b, _ := os.ReadFile(filepath.Join(external, "SKILL.md"))
			if string(b) != withMarker("external") {
				t.Fatal("changed external file")
			}
		})
	}
}

func TestApplyDoesNotFollowTemporarySymlink(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "af-environment", "SKILL.md")
	write(t, path, withMarker("old"))
	external := filepath.Join(t.TempDir(), "user-file")
	write(t, external, "keep")
	if err := os.Symlink(external, path+".af-tmp"); err != nil {
		t.Fatal(err)
	}
	if err := Apply(root, map[string]string{"environment": topic}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(external)
	if string(b) != "keep" {
		t.Fatal("temporary symlink target overwritten")
	}
}

func TestApplyRejectsInvalidStemBeforePruning(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "af-old", "SKILL.md")
	write(t, path, withMarker("old"))
	if err := Apply(root, map[string]string{"x/../../outside": topic}); err == nil {
		t.Fatal("invalid stem accepted")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("pruned before validating: %v", err)
	}
}

func TestPruneDoesNotTreatQuotedOrLinkedMarkerAsOwnership(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "af-quoted", "SKILL.md"), "Example:\n"+Marker+"\n")
	external := filepath.Join(t.TempDir(), "SKILL.md")
	write(t, external, withMarker("external"))
	if err := os.Mkdir(filepath.Join(root, "af-linked"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "af-linked", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err := Apply(root, map[string]string{"environment": topic}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"af-quoted", "af-linked"} {
		if _, err := os.Lstat(filepath.Join(root, name, "SKILL.md")); err != nil {
			t.Fatalf("removed %s: %v", name, err)
		}
	}
}
