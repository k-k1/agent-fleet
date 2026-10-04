package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// putFile writes a placeholder file (the walk never opens one) with a fixed mtime, so the
// newest-first order is decided by the test, not by how fast it ran.
func putFile(t *testing.T, root, rel string, age time.Duration) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-age)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

func imagesRequest(t *testing.T, query url.Values) (int, fsImagesResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	handleFSImages(rec, httptest.NewRequest("GET", "/fs/images?"+query.Encode(), nil))
	var out fsImagesResponse
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, out
}

func names(es []fsEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Name
	}
	return out
}

func sameNames(t *testing.T, got []fsEntry, want ...string) {
	t.Helper()
	g := names(got)
	if len(g) != len(want) {
		t.Fatalf("names = %q, want %q", g, want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("names = %q, want %q", g, want)
		}
	}
}

// The pictures under the folder, several levels down, newest first, each named relative to
// the folder asked for so folder + "/" + name is its browse path. Non-images and hidden
// subfolders are left out; the depth bound stops the walk.
func TestFSImagesFlattensNewestFirst(t *testing.T) {
	root := thumbRoots(t)
	putFile(t, root, "gen/top.png", 5*time.Minute)
	putFile(t, root, "gen/a/one.jpg", 1*time.Minute)
	putFile(t, root, "gen/a/b/two.webp", 3*time.Minute)
	putFile(t, root, "gen/a/b/c/deep.png", 0)
	putFile(t, root, "gen/a/notes.txt", 0)
	putFile(t, root, "gen/.git/objects/pic.png", 0)

	code, got := imagesRequest(t, url.Values{"path": {"gen"}, "depth": {"2"}})
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	sameNames(t, got.Entries, "a/one.jpg", "a/b/two.webp", "top.png")
	if got.Truncated {
		t.Error("a depth bound is the reader's request, not a truncation")
	}
	if got.Path != "gen" || got.Entries[0].Mtime == 0 || got.Entries[0].Size != 1 || got.Entries[0].Type != "file" {
		t.Errorf("entry = %+v (path %q)", got.Entries[0], got.Path)
	}

	_, deeper := imagesRequest(t, url.Values{"path": {"gen"}, "depth": {"3"}})
	sameNames(t, deeper.Entries, "a/b/c/deep.png", "a/one.jpg", "a/b/two.webp", "top.png")
}

// `limit` keeps the newest and says the rest were left out.
func TestFSImagesLimitTruncates(t *testing.T) {
	root := thumbRoots(t)
	putFile(t, root, "g/old.png", 3*time.Minute)
	putFile(t, root, "g/s/new.png", 1*time.Minute)
	putFile(t, root, "g/mid.png", 2*time.Minute)
	_, got := imagesRequest(t, url.Values{"path": {"g"}, "limit": {"2"}})
	sameNames(t, got.Entries, "s/new.png", "mid.png")
	if !got.Truncated {
		t.Error("truncated = false with a picture left out")
	}
}

// The walk never leaves the folder: a symlink — to a folder outside the root, to a denylisted
// one inside it, or to a picture — is not followed or listed, and the denylist holds for a
// path met on the way down, not only for the one asked for.
func TestFSImagesStaysInside(t *testing.T) {
	root := thumbRoots(t)
	outside := t.TempDir()
	putFile(t, outside, "secret.png", 0)
	putFile(t, root, ".ssh/key.png", 0)
	putFile(t, root, ".local/share/agent-fleet/scrollback.png", 0)
	putFile(t, root, ".local/share/ok.png", 0)
	putFile(t, root, "pics/real.png", time.Minute)
	for link, target := range map[string]string{
		"pics/out":       outside,
		"pics/ssh":       filepath.Join(root, ".ssh"),
		"pics/alias.png": filepath.Join(outside, "secret.png"),
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}

	_, got := imagesRequest(t, url.Values{"path": {"pics"}})
	sameNames(t, got.Entries, "real.png")

	// A hidden start is walked (that is how the generated root is reached), and the denylist
	// still stops the walk at .local/share/agent-fleet on the way down.
	_, local := imagesRequest(t, url.Values{"path": {".local"}})
	sameNames(t, local.Entries, "share/ok.png")

	for _, bad := range []string{"../", "pics/../../x", ".ssh", "pics/out"} {
		if code, _ := imagesRequest(t, url.Values{"path": {bad}}); code != 400 {
			t.Errorf("path %q: status = %d, want 400", bad, code)
		}
	}
	if code, _ := imagesRequest(t, url.Values{"path": {"pics/real.png"}}); code != 404 {
		t.Errorf("a file as the folder: status = %d, want 404", code)
	}
}

// Out-of-range values are clamped, not refused: the parameters are advisory, like thumb.
func TestBoundedQueryInt(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want int
	}{{"", 3}, {"x", 3}, {"0", 1}, {"-4", 1}, {"2", 2}, {"99", 6}} {
		if got := boundedQueryInt(c.raw, 3, 6); got != c.want {
			t.Errorf("boundedQueryInt(%q) = %d, want %d", c.raw, got, c.want)
		}
	}
}

// A start reached through a symlink inside the root (alias -> .local/share) must not list what
// the denylist hides under the folder it resolves to, though the names stay alias-relative.
func TestFSImagesAliasedStartKeepsDenylist(t *testing.T) {
	root := thumbRoots(t)
	putFile(t, root, ".local/share/agent-fleet/private.png", 0)
	putFile(t, root, ".local/share/public.png", 0)
	if err := os.Symlink(filepath.Join(root, ".local", "share"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	code, got := imagesRequest(t, url.Values{"path": {"alias"}})
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	sameNames(t, got.Entries, "public.png")
}

// A browse root that is itself a symlink keeps working, and keeps its denylist.
func TestFSImagesSymlinkedBrowseRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_BROWSE_ROOT", link)
	putFile(t, real, ".local/share/agent-fleet/private.png", 0)
	putFile(t, real, ".local/share/ok.png", 0)
	putFile(t, real, "pics/a.png", 0)
	_, local := imagesRequest(t, url.Values{"path": {".local"}})
	sameNames(t, local.Entries, "share/ok.png")
	_, pics := imagesRequest(t, url.Values{"path": {"pics"}})
	sameNames(t, pics.Entries, "a.png")
}

// One huge folder is read in batches no larger than what is left of the entry budget — never
// whole — and the walk stops at the budget, marked truncated.
func TestFSImagesHugeFolderIsReadInBoundedBatches(t *testing.T) {
	root := thumbRoots(t)
	for i := 0; i < 120; i++ {
		putFile(t, root, "big/"+strconv.Itoa(i)+".png", 0)
	}
	oldMax, oldBatch, oldRead := imagesMaxFiles, imagesReadBatch, imagesReadDir
	t.Cleanup(func() { imagesMaxFiles, imagesReadBatch, imagesReadDir = oldMax, oldBatch, oldRead })
	imagesMaxFiles, imagesReadBatch = 50, 16
	var asked []int
	imagesReadDir = func(f *os.File, n int) ([]os.DirEntry, error) {
		asked = append(asked, n)
		return f.ReadDir(n)
	}
	code, got := imagesRequest(t, url.Values{"path": {"big"}})
	if code != 200 || !got.Truncated || len(got.Entries) != 50 {
		t.Fatalf("status %d, truncated %v, %d entries; want 200, true, 50", code, got.Truncated, len(got.Entries))
	}
	total := 0
	for _, n := range asked {
		if n < 1 || n > 16 {
			t.Errorf("asked ReadDir(%d), want 1..16", n)
		}
		total += n
	}
	if total > 50 {
		t.Errorf("asked for %d entries in total (%v), want at most the budget of 50", total, asked)
	}
}

// The walk checks its context between batches: a deadline or a reader that went away stops it.
func TestFSImagesStopsWhenContextEnds(t *testing.T) {
	root := thumbRoots(t)
	for i := 0; i < 40; i++ {
		putFile(t, root, "g/"+strconv.Itoa(i)+".png", 0)
	}
	oldBatch, oldRead := imagesReadBatch, imagesReadDir
	t.Cleanup(func() { imagesReadBatch, imagesReadDir = oldBatch, oldRead })
	imagesReadBatch = 10

	ctx, cancel := context.WithCancel(context.Background())
	imagesReadDir = func(f *os.File, n int) ([]os.DirEntry, error) {
		defer cancel() // the reader leaves after the first batch
		return f.ReadDir(n)
	}
	got, truncated := walkImages(ctx, filepath.Join(root, "g"), "g", imagesDenyBase(filepath.Join(root, "g")), 3)
	if !truncated || len(got) != 10 {
		t.Fatalf("cancelled walk: %d entries, truncated %v; want 10, true", len(got), truncated)
	}

	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	imagesReadDir = oldRead
	got, truncated = walkImages(expired, filepath.Join(root, "g"), "g", nil, 3)
	if !truncated || len(got) != 0 {
		t.Fatalf("expired walk: %d entries, truncated %v; want 0, true", len(got), truncated)
	}
}

// With every walk slot taken, a request waits briefly and then answers 503 instead of piling on.
func TestFSImagesBusyWhenSlotsTaken(t *testing.T) {
	root := thumbRoots(t)
	putFile(t, root, "g/a.png", 0)
	oldWait := imagesSlotWait
	t.Cleanup(func() { imagesSlotWait = oldWait })
	imagesSlotWait = 20 * time.Millisecond
	for i := 0; i < cap(imagesWalkSlots); i++ {
		imagesWalkSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(imagesWalkSlots); i++ {
			<-imagesWalkSlots
		}
	}()
	if code, _ := imagesRequest(t, url.Values{"path": {"g"}}); code != 503 {
		t.Fatalf("status = %d, want 503", code)
	}
}

// fs/tree has the same gate and the same hole: listing (and peeking into) a start reached
// through an alias must not reveal what the denylist hides under the folder it resolves to.
func TestFSTreeAliasedStartKeepsDenylist(t *testing.T) {
	root := thumbRoots(t)
	putFile(t, root, ".local/share/agent-fleet/private.png", 0)
	putFile(t, root, ".local/share/pub/ok.png", 0)
	if err := os.Symlink(filepath.Join(root, ".local", "share"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handleFSTree(rec, httptest.NewRequest("GET", "/fs/tree?path=alias&peek=4", nil))
	var out struct{ Entries []fsEntry }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	sameNames(t, out.Entries, "pub")
	if len(out.Entries[0].Preview) != 1 || out.Entries[0].Preview[0].Name != "ok.png" {
		t.Errorf("pub preview = %+v", out.Entries[0].Preview)
	}
}
