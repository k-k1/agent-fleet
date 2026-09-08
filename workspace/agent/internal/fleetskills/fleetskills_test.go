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
