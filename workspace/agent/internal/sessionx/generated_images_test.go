package sessionx

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/imagegen"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// generatedImagesFixture stands up a session whose generated-image folder holds the given
// names, and returns the meta plus the folder. Everything lives under the test's own HOME,
// which is also the browse root the sessionx test wiring falls back to.
func generatedImagesFixture(t *testing.T, name string, files ...string) (session.Meta, string) {
	t.Helper()
	withTempHome(t)
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(m)
	dir := imagegen.GeneratedDir(session.UUID(m.Dir, m.Name))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return m, dir
}

// The session row carries the count and the folder, so the Console's gallery entry can decide
// whether to offer itself at all and then open the folder with no further round trip
// (ADR 0080 decision 8). The path is browse-root relative: an absolute one is not something
// the file API accepts from the tree.
func TestWireSessionCarriesGeneratedImagesCountAndPath(t *testing.T) {
	m, _ := generatedImagesFixture(t, "gen1",
		"image-1-1.png", "image-1-2.jpg", "image-2-1.webp",
		// Not images, and not counted: imagegen's half-written temporary, and a note.
		".image-9999", "notes.txt")

	got := wireSession(m, true)
	if got.GeneratedImages != 3 {
		t.Fatalf("generatedImages = %d, want 3 (only the image extensions count)", got.GeneratedImages)
	}
	want := ".cache/agent-fleet/generated/" + session.UUID(m.Dir, m.Name)
	if got.GeneratedImagesPath != want {
		t.Fatalf("generatedImagesPath = %q, want %q (browse-root relative)", got.GeneratedImagesPath, want)
	}
	// A stopped row carries them too: the images outlive the session by up to 30 days and the
	// gallery reads them through the file API, which does not care whether the session runs.
	if stopped := wireSession(m, false); stopped.GeneratedImages != 3 {
		t.Fatalf("stopped row's generatedImages = %d, want 3", stopped.GeneratedImages)
	}
}

// Nothing generated = neither field on the wire. Both are omitempty, so a deployment that has
// never generated an image pays no bytes for this, and the menu item stays away.
func TestWireSessionOmitsGeneratedImagesWhenThereAreNone(t *testing.T) {
	// No folder at all — the common case.
	withTempHome(t)
	none := session.Meta{Name: "gen-none", Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(none)
	if got := wireSession(none, true); got.GeneratedImages != 0 || got.GeneratedImagesPath != "" {
		t.Fatalf("a session that never generated reported %d / %q", got.GeneratedImages, got.GeneratedImagesPath)
	}

	// A folder left behind by the retention sweep, holding no image. The path must go with the
	// count: an entry that opens an empty gallery is worse than no entry.
	empty, _ := generatedImagesFixture(t, "gen-empty", "notes.txt")
	if got := wireSession(empty, true); got.GeneratedImages != 0 || got.GeneratedImagesPath != "" {
		t.Fatalf("an image-less folder reported %d / %q", got.GeneratedImages, got.GeneratedImagesPath)
	}
}

// The count is memoised on the folder's mtime, because the listing counts once per session row
// and a folder holds up to 30 days of images. This pins both halves: the same mtime is not
// re-read, and a moved mtime is.
func TestGeneratedImagesCountIsMemoisedOnTheFolderMtime(t *testing.T) {
	m, dir := generatedImagesFixture(t, "gen-memo", "image-1-1.png")
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	frozen := fi.ModTime()
	if n, _ := generatedImages(m); n != 1 {
		t.Fatalf("first count = %d, want 1", n)
	}

	// Add an image and put the folder's mtime back where it was: the only way to tell a cached
	// answer from a fresh one is to make the two differ.
	if err := os.WriteFile(filepath.Join(dir, "image-2-1.png"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, frozen, frozen); err != nil {
		t.Fatal(err)
	}
	if n, _ := generatedImages(m); n != 1 {
		t.Fatalf("count = %d with the folder's mtime unchanged, want the memoised 1 (the ReadDir was not skipped)", n)
	}

	moved := frozen.Add(2 * time.Second)
	if err := os.Chtimes(dir, moved, moved); err != nil {
		t.Fatal(err)
	}
	if n, _ := generatedImages(m); n != 2 {
		t.Fatalf("count = %d after the folder's mtime moved, want 2 (the memo went stale)", n)
	}
}
