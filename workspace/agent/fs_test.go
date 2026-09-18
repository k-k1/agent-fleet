package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSafeBrowsePath covers path resolution for the read-only file browser. Relative paths
// stay anchored on the browse root (denylist + traversal enforced); absolute paths — the
// form SendUserFile leaves for a file outside the browse root — are served only when they
// sit under an allowed read root (the browse root, the /tmp/claude-<uid> scratch base,
// or the role-scoped Agent Fleet docs mount), so a shared scratchpad or user guide opens.
func TestSafeBrowsePath(t *testing.T) {
	root := "/home/testuser"
	t.Setenv("AF_BROWSE_ROOT", root)

	scratch := scratchRoot() // /tmp/claude-<uid> — same base the harness uses for scratchpads

	cases := []struct {
		name, p  string
		wantFull string
		wantRel  string
		wantOK   bool
	}{
		// relative (browse-root-relative) — unchanged behavior
		{"rel under root", "repos/x/a.png", root + "/repos/x/a.png", "repos/x/a.png", true},
		{"rel root itself", "", root, "", true},
		{"rel traversal escapes", "../etc/passwd", "", "", false},
		{"rel denied", ".ssh/id_rsa", "", "", false},

		// absolute under the browse root → served, display path is home-relative
		{"abs under root", root + "/repos/x/a.png", root + "/repos/x/a.png", "repos/x/a.png", true},
		{"abs denied under root", root + "/.config/agent-fleet/store", "", "", false},
		// The state half of the same tree (ADR 0087 decision 4) — session ledger, chat
		// working dirs, pending-permission payloads — is denied by the same rule.
		{"abs denied in state dir", root + "/.local/state/agent-fleet/sessions/slot01.json", "", "", false},
		{"rel denied in state dir", ".local/state/agent-fleet/chat-wd", "", "", false},

		// absolute under the scratch base → served, display path is the absolute path
		{"abs in scratch", scratch + "/sess/scratchpad/compact-preview.png", scratch + "/sess/scratchpad/compact-preview.png", scratch + "/sess/scratchpad/compact-preview.png", true},
		{"abs in staged docs", agentFleetDocsRoot() + "/guide/member/README.md", agentFleetDocsRoot() + "/guide/member/README.md", agentFleetDocsRoot() + "/guide/member/README.md", true},
		{"abs in codex generated images", codexGeneratedImagesRoot() + "/job/image.png", codexGeneratedImagesRoot() + "/job/image.png", codexGeneratedImagesRoot() + "/job/image.png", true},

		// absolute outside every allowed root → refused
		{"abs outside all", "/etc/passwd", "", "", false},
		{"abs scratch traversal escapes", scratch + "/../../etc/passwd", "", "", false},
	}
	for _, c := range cases {
		full, rel, ok := safeBrowsePath(c.p)
		if ok != c.wantOK {
			t.Errorf("%s: safeBrowsePath(%q) ok = %v, want %v", c.name, c.p, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if full != filepath.Clean(c.wantFull) || rel != c.wantRel {
			t.Errorf("%s: safeBrowsePath(%q) = (%q, %q), want (%q, %q)", c.name, c.p, full, rel, filepath.Clean(c.wantFull), c.wantRel)
		}
	}
}

func TestSafeWritableBrowsePathRefusesReadOnlyRoots(t *testing.T) {
	t.Setenv("AF_BROWSE_ROOT", t.TempDir())
	for _, p := range []string{scratchRoot() + "/note.txt", agentFleetDocsRoot() + "/guide/member/README.md"} {
		if _, _, ok := safeWritableBrowsePath(p); ok {
			t.Errorf("safeWritableBrowsePath(%q) unexpectedly allowed an absolute read-only path", p)
		}
	}
}

// TestHandleFSFileScratchpad drives the real HTTP handler end-to-end: a file written under
// the scratch base (where SendUserFile shares a compact preview) is served with its content,
// where before the fix the absolute /tmp path collapsed to $HOME/tmp/... and 404'd.
func TestHandleFSFileScratchpad(t *testing.T) {
	t.Setenv("AF_BROWSE_ROOT", t.TempDir()) // an unrelated home, to prove the file is served via the scratch root

	// A real file under scratchRoot(), the base the harness uses for per-session scratchpads.
	dir := filepath.Join(scratchRoot(), "aftest-"+t.Name())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	abs := filepath.Join(dir, "compact-preview.txt")
	const want = "shared scratch content"
	if err := os.WriteFile(abs, []byte(want), 0o600); err != nil {
		t.Fatalf("write scratch file: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/fs/file?path="+url.QueryEscape(abs), nil)
	handleFSFile(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rr.Body.String())
	}
	if resp.Content != want {
		t.Errorf("content = %q, want %q", resp.Content, want)
	}
	if resp.Path != abs {
		t.Errorf("path = %q, want %q (absolute, so the viewer round-trips the same key)", resp.Path, abs)
	}
}

func TestHandleFSFileCodexGeneratedImageSharedRelativeToBrowseRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	t.Setenv("CODEX_HOME", filepath.Join(root, ".codex"))

	dir := filepath.Join(codexGeneratedImagesRoot(), "job")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir generated images: %v", err)
	}
	image := filepath.Join(dir, "image.png")
	if err := os.WriteFile(image, []byte("PNG"), 0o600); err != nil {
		t.Fatalf("write generated image: %v", err)
	}

	rel := ".codex/generated_images/job/image.png"
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/fs/file?path="+url.QueryEscape(rel), nil)
	handleFSFile(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rr.Body.String())
	}
	if resp.Path != rel || resp.Content != "PNG" {
		t.Errorf("response = (%q, %q), want (%q, %q)", resp.Path, resp.Content, rel, "PNG")
	}
}

// The tree carries each entry's mtime, on directories as well as files: "newest first" and
// "3 minutes ago" have no other source (ADR 0080 decision 2). Directories are included
// because the same Info() call already has the answer, and a folder-first sort is the next
// thing anyone asks for. Denylisted names stay out of the listing, mtime or not.
func TestFSTreeCarriesMtimeOnFilesAndDirs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	if err := os.MkdirAll(filepath.Join(root, "shots"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.png"), []byte("PNG"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A denylisted folder with something inside it, to prove mtime did not open a door.
	if err := os.MkdirAll(filepath.Join(root, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Pin both entries to a known time, so the assertion is on the value and not merely on
	// "some number showed up".
	when := time.Date(2026, 9, 13, 10, 30, 0, 0, time.Local)
	for _, p := range []string{filepath.Join(root, "shots"), filepath.Join(root, "a.png")} {
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}

	rr := httptest.NewRecorder()
	handleFSTree(rr, httptest.NewRequest(http.MethodGet, "/api/fs/tree?path=", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Entries []fsEntry `json:"entries"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rr.Body.String())
	}
	got := map[string]fsEntry{}
	for _, e := range resp.Entries {
		got[e.Name] = e
	}
	if _, denied := got[".ssh"]; denied {
		t.Error("the listing enumerated a denylisted entry")
	}
	for _, name := range []string{"shots", "a.png"} {
		e, ok := got[name]
		if !ok {
			t.Fatalf("entry %q missing from the listing: %#v", name, resp.Entries)
		}
		if e.Mtime != when.Unix() {
			t.Errorf("%s: mtime = %d, want %d (unix seconds)", name, e.Mtime, when.Unix())
		}
	}
	if got["shots"].Type != "dir" || got["a.png"].Size != 3 {
		t.Errorf("mtime cost the entries their type/size: %#v", got)
	}
}

// --- peek: what is inside a folder card ----------------------------------------------
//
// One `fs/tree` per folder card is what decision 9 refused, so the cover and the count ride on
// the listing that already described the folder. Everything here is about the two rules that
// make that safe: it costs nothing unless asked for, and it never widens what is visible.

// treeEntries runs a listing and returns its entries by name.
func treeEntries(t *testing.T, query string) map[string]fsEntry {
	t.Helper()
	rec := httptest.NewRecorder()
	handleFSTree(rec, httptest.NewRequest("GET", "/api/fs/tree?"+query, nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Entries []fsEntry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	out := map[string]fsEntry{}
	for _, e := range resp.Entries {
		out[e.Name] = e
	}
	return out
}

// imageNamed writes a file that COUNTS as a picture. peek decides by extension (filemeta,
// decision 3) and never opens the file, so this needs no pixels — only the warming test below
// needs a decodable one.
func imageNamed(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("stand-in for a picture"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// touch pins a file's mtime, so "the newest one" is asserted on a value rather than on
// whatever order the filesystem happened to hand back.
func touch(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func TestFSTreePeekDescribesSubfolders(t *testing.T) {
	root := thumbRoots(t)
	sub := filepath.Join(root, "gen")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 9, 15, 9, 0, 0, 0, time.Local)
	for i, name := range []string{"old.png", "new.png", "mid.png"} {
		imageNamed(t, filepath.Join(sub, name))
		touch(t, filepath.Join(sub, name), when.Add(time.Duration(i)*time.Hour))
	}
	// Not a picture, and a picture in a denylisted folder: neither may be counted or shown.
	if err := os.WriteFile(filepath.Join(sub, "notes.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	imageNamed(t, filepath.Join(root, ".ssh", "secret.png"))
	touch(t, sub, when) // the memo key: pinned so the read below is the one under test

	got := treeEntries(t, "path=&peek=1")
	gen, ok := got["gen"]
	if !ok {
		t.Fatalf("the subfolder is missing from the listing: %#v", got)
	}
	if gen.Images != 3 {
		t.Errorf("images = %d, want 3 (notes.md is not a picture)", gen.Images)
	}
	if len(gen.Preview) != 1 {
		t.Fatalf("preview = %#v, want exactly the one cover that was asked for", gen.Preview)
	}
	// "mid.png" is the newest by mtime; its NAME sorts in the middle, which is the point —
	// generated names carry a timestamp but a folder of screenshots does not.
	if gen.Preview[0].Name != "mid.png" {
		t.Errorf("cover = %q, want mid.png (the newest by mtime)", gen.Preview[0].Name)
	}
	if want := when.Add(2 * time.Hour).Unix(); gen.Preview[0].Mtime != want {
		t.Errorf("cover mtime = %d, want %d — without it the URL carries no version and every card re-asks",
			gen.Preview[0].Mtime, want)
	}
	if _, denied := got[".ssh"]; denied {
		t.Error("peeking enumerated a denylisted folder")
	}
}

func TestFSTreePeekIsOptionalAndBounded(t *testing.T) {
	root := thumbRoots(t)
	sub := filepath.Join(root, "gen")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	imageNamed(t, filepath.Join(sub, "a.png"))

	// No parameter: the file tree lists code folders constantly and must not pay for any of
	// this — nor may an old reader start seeing fields it never asked for.
	if e := treeEntries(t, "path=")["gen"]; e.Images != 0 || e.Preview != nil {
		t.Errorf("a plain listing described the folder's contents: %#v", e)
	}
	// Out of range is "no" rather than an error, the same rule as thumb= (fs_thumb.go).
	for _, q := range []string{"peek=0", "peek=9", "peek=lots", "peek=-1"} {
		if e := treeEntries(t, "path=&"+q)["gen"]; e.Preview != nil {
			t.Errorf("%s was honoured: %#v", q, e)
		}
	}
	// A picture asked for more than exists comes back with what there is, not padding.
	if e := treeEntries(t, "path=&peek=4")["gen"]; len(e.Preview) != 1 {
		t.Errorf("peek=4 over a one-picture folder = %#v", e.Preview)
	}
}

// The memo is keyed on the folder's own mtime — which is what changes when a picture lands.
// A memo that answered from the first read forever would mean a session's newest generation
// never becomes its cover.
func TestFSTreePeekFollowsTheFolder(t *testing.T) {
	root := thumbRoots(t)
	sub := filepath.Join(root, "gen")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 9, 15, 9, 0, 0, 0, time.Local)
	imageNamed(t, filepath.Join(sub, "first.png"))
	touch(t, filepath.Join(sub, "first.png"), when)
	touch(t, sub, when)
	if e := treeEntries(t, "path=&peek=1")["gen"]; e.Preview[0].Name != "first.png" {
		t.Fatalf("cover = %q, want first.png", e.Preview[0].Name)
	}

	imageNamed(t, filepath.Join(sub, "second.png"))
	touch(t, filepath.Join(sub, "second.png"), when.Add(time.Hour))
	touch(t, sub, when.Add(time.Hour)) // adding an entry moves the directory's own mtime
	e := treeEntries(t, "path=&peek=1")["gen"]
	if e.Preview[0].Name != "second.png" || e.Images != 2 {
		t.Errorf("after a picture landed: cover=%q images=%d, want second.png and 2", e.Preview[0].Name, e.Images)
	}
}

// The covers live in OTHER folders, so warming the listed one does not reach them: a folder
// page (the generated root is exactly that) would otherwise decode every cover cold.
func TestFSTreePeekWarmsTheCovers(t *testing.T) {
	root := thumbRoots(t)
	sub := filepath.Join(root, "gen")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	cover := filepath.Join(sub, "a.png")
	noisyPNG(t, cover, 800, 600, false)

	treeEntries(t, "path=&peek=1&warm=64")
	if !waitForCache(t, cover, 64) {
		t.Fatal("the cover was not warmed: the folder page answered but its picture is still cold")
	}
}

// peekDir re-checks the denylist against each child's browse-relative path. The listing above
// it already refuses a denied FOLDER, so this is the rule for a denied FILE inside a folder
// that is otherwise fine — no entry of fsDeny is an image today, which is exactly why the
// check needs a test of its own rather than a reader's trust.
//
// Two directories, not one read twice: the memo is keyed on the absolute path (one folder has
// exactly one browse-relative form), so re-reading the same folder under a different `rel` is
// not a thing the caller can do — and faking it here would test the memo, not the denylist.
func TestPeekDirHonoursTheDenylistPerChild(t *testing.T) {
	plain, denied := t.TempDir(), t.TempDir()
	for _, dir := range []string{plain, denied} {
		if err := os.WriteFile(filepath.Join(dir, "x.png"), []byte("stand-in"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mod := func(dir string) time.Time {
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		return fi.ModTime()
	}
	if got := peekDir(plain, "", mod(plain)); got.images != 1 {
		t.Fatalf("images = %d in a folder that is not denied, want 1", got.images)
	}
	if got := peekDir(denied, ".ssh", mod(denied)); got.images != 0 || len(got.preview) != 0 {
		t.Errorf("a denied child was described: %#v", got)
	}
}
