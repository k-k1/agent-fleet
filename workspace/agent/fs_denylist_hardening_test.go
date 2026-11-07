package main

// Denylist hardening for the file browser's mutating and secondary paths: ancestors of denied
// entries, symlinks, and the unbounded line-marks read. Each refused case has an allowed twin so
// a gate that simply refuses everything cannot pass.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func hardeningHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_BROWSE_ROOT", home)
	mustMkdir(t, filepath.Join(home, ".config", "agent-fleet"))
	mustWrite(t, filepath.Join(home, ".config", "agent-fleet", "store"), "secret")
	mustMkdir(t, filepath.Join(home, ".local", "share", "agent-fleet"))
	mustWrite(t, filepath.Join(home, ".local", "share", "agent-fleet", "ledger"), "secret")
	mustMkdir(t, filepath.Join(home, "docs"))
	mustWrite(t, filepath.Join(home, "docs", "a.txt"), "a")
	return home
}

func fsCall(h http.HandlerFunc, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, target, nil))
	return rec
}

func TestIsDeniedOrAncestor(t *testing.T) {
	for rel, want := range map[string]bool{
		"": true, ".config": true, ".local": true, ".local/share": true,
		".config/agent-fleet": true, ".config/agent-fleet/x": true, ".ssh/id": true,
		"docs": false, ".config/other": false, ".local/bin": false, ".configx": false, "repos/x": false,
	} {
		if got := isDeniedOrAncestor(rel); got != want {
			t.Errorf("isDeniedOrAncestor(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestFSRenameRefusesAncestorsOfDeniedEntries(t *testing.T) {
	home := hardeningHome(t)
	for _, c := range []struct{ from, to string }{
		{".config", "moved"},
		{".local/share", "moved"},
		{".local", "moved"},
		{"docs", ".local/share"},
	} {
		rec := fsCall(handleFSRename, "/fs/rename?from="+c.from+"&to="+c.to)
		if rec.Code != http.StatusForbidden {
			t.Errorf("rename %s -> %s: status %d, want 403 (%s)", c.from, c.to, rec.Code, rec.Body.String())
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "agent-fleet", "store")); err != nil {
		t.Errorf("denied state moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "moved")); err == nil {
		t.Error("an ancestor was renamed into the readable tree")
	}
	// An alias that resolves to an ancestor is refused too.
	if err := os.Symlink(filepath.Join(home, ".local"), filepath.Join(home, "alias")); err != nil {
		t.Fatal(err)
	}
	if rec := fsCall(handleFSRename, "/fs/rename?from=alias/share&to=moved"); rec.Code != http.StatusForbidden {
		t.Errorf("rename via alias: status %d, want 403", rec.Code)
	}
}

func TestFSRenameStillMovesOrdinaryFolders(t *testing.T) {
	home := hardeningHome(t)
	if rec := fsCall(handleFSRename, "/fs/rename?from=docs&to=docs2"); rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(home, "docs2", "a.txt")); err != nil {
		t.Error(err)
	}
}

func TestFSDeleteRefusesAncestorsOfDeniedEntries(t *testing.T) {
	home := hardeningHome(t)
	if err := os.Symlink(filepath.Join(home, ".local"), filepath.Join(home, "alias")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".config", ".local", ".local/share", "alias/share"} {
		if rec := fsCall(handleFSDelete, "/fs/delete?path="+p); rec.Code != http.StatusForbidden {
			t.Errorf("delete %s: status %d, want 403 (%s)", p, rec.Code, rec.Body.String())
		}
	}
	for _, f := range []string{".config/agent-fleet/store", ".local/share/agent-fleet/ledger"} {
		if _, err := os.Stat(filepath.Join(home, f)); err != nil {
			t.Errorf("%s was removed: %v", f, err)
		}
	}
}

func TestFSDeleteStillRemovesOrdinaryFolders(t *testing.T) {
	home := hardeningHome(t)
	if rec := fsCall(handleFSDelete, "/fs/delete?path=docs"); rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(home, "docs")); err == nil {
		t.Error("docs still exists")
	}
}

func TestFSMkdirAndNewFileRefuseAncestors(t *testing.T) {
	hardeningHome(t)
	for _, p := range []string{".local/share", ".config"} {
		if rec := fsCall(handleFSMkdir, "/fs/mkdir?path="+p); rec.Code == http.StatusOK {
			t.Errorf("mkdir %s succeeded", p)
		}
	}
	if rec := fsCall(handleFSNewFile, "/fs/newfile?path=docs/n.txt"); rec.Code != http.StatusOK {
		t.Errorf("new file in an ordinary folder: %d", rec.Code)
	}
	if rec := fsCall(handleFSMkdir, "/fs/mkdir?path=docs/sub"); rec.Code != http.StatusOK {
		t.Errorf("mkdir in an ordinary folder: %d", rec.Code)
	}
}

func TestFSUploadRefusesSymlinkedFolderThatResolvesToRoot(t *testing.T) {
	home := hardeningHome(t)
	if err := os.Symlink(home, filepath.Join(home, "lnk")); err != nil {
		t.Fatal(err)
	}
	rec := uploadOnePNG(t, "lnk", ".claude.json")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); err == nil {
		t.Error("a denied name was written through the symlinked folder")
	}
	// A symlink to a denied folder stays refused.
	mustMkdir(t, filepath.Join(home, ".ssh"))
	if err := os.Symlink(filepath.Join(home, ".ssh"), filepath.Join(home, "lnk2")); err != nil {
		t.Fatal(err)
	}
	if rec := uploadOnePNG(t, "lnk2", "k.png"); rec.Code == http.StatusOK {
		t.Error("upload through a link to a denied folder succeeded")
	}
	// Uploading into an ancestor of denied state is refused as well.
	if rec := uploadOnePNG(t, ".config", "x.png"); rec.Code != http.StatusForbidden {
		t.Errorf("upload into .config: %d, want 403", rec.Code)
	}
}

func TestFSUploadWritesIntoOrdinaryFoldersAndKeepsConflictRules(t *testing.T) {
	home := hardeningHome(t)
	if rec := uploadOnePNG(t, "docs", "new.png"); rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body.String())
	}
	if b, err := os.ReadFile(filepath.Join(home, "docs", "new.png")); err != nil || len(b) == 0 {
		t.Fatalf("not written: %v", err)
	}
	if rec := uploadOnePNG(t, "docs", "new.png"); rec.Code != http.StatusConflict {
		t.Errorf("second upload: %d, want 409", rec.Code)
	}
	if rec := uploadOnePNG(t, "docs&overwrite=1", "new.png"); rec.Code != http.StatusOK {
		t.Errorf("overwrite: %d", rec.Code)
	}
	// The home folder itself, spelled as such, still takes uploads.
	if rec := uploadOnePNG(t, "", "top.png"); rec.Code != http.StatusOK {
		t.Errorf("upload to home: %d (%s)", rec.Code, rec.Body.String())
	}
	if m, _ := filepath.Glob(filepath.Join(home, "docs", ".af-edit-*")); len(m) != 0 {
		t.Errorf("temp files left behind: %v", m)
	}
}

func TestWritableResolvedGateForGeneratedOutDir(t *testing.T) {
	home := hardeningHome(t)
	if err := os.Symlink(home, filepath.Join(home, "lnk")); err != nil {
		t.Fatal(err)
	}
	mustMkdir(t, filepath.Join(home, ".ssh"))
	if err := os.Symlink(filepath.Join(home, ".ssh"), filepath.Join(home, "lnk2")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"lnk", "lnk2", ".config", ".local/share"} {
		if _, _, ok := safeWritableResolvedBrowsePath(p); ok {
			t.Errorf("%s was accepted as an output folder", p)
		}
	}
	for _, p := range []string{"docs", "generated/new/folder"} {
		if _, _, ok := safeWritableResolvedBrowsePath(p); !ok {
			t.Errorf("%s was refused", p)
		}
	}
}

func TestWarmThumbRefusesSymlinksAndDeniedSources(t *testing.T) {
	root := thumbRoots(t)
	real := filepath.Join(root, "gen", "shot.png")
	mustMkdir(t, filepath.Dir(real))
	noisyPNG(t, real, 800, 600, false)
	link := filepath.Join(root, "gen", "link.png")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	mustMkdir(t, filepath.Join(root, ".ssh"))
	denied := filepath.Join(root, ".ssh", "pic.png")
	noisyPNG(t, denied, 800, 600, false)

	cached := func() int {
		m, _ := filepath.Glob(filepath.Join(thumbCacheDir(), "*"))
		return len(m)
	}
	warmThumbFile(link, 64)
	warmThumbFile(denied, 64)
	warmPreviewFile(link)
	if n := cached(); n != 0 {
		t.Fatalf("%d cache entries written for a symlink or denied source", n)
	}
	warmThumbFile(real, 64)
	if n := cached(); n != 1 {
		t.Fatalf("%d cache entries for an ordinary image, want 1", n)
	}
}

func TestWarmThumbDirSkipsSymlinkedPictures(t *testing.T) {
	root := thumbRoots(t)
	dir := filepath.Join(root, "gen")
	mustMkdir(t, dir)
	other := filepath.Join(root, "elsewhere.png")
	noisyPNG(t, other, 800, 600, false)
	if err := os.Symlink(other, filepath.Join(dir, "link.png")); err != nil {
		t.Fatal(err)
	}
	warmThumbDir(dir, 64)
	if m, _ := filepath.Glob(filepath.Join(thumbCacheDir(), "*")); len(m) != 0 {
		t.Errorf("symlinked picture was warmed: %v", m)
	}
}

func TestPeekDirIgnoresSymlinkedPictures(t *testing.T) {
	root := thumbRoots(t)
	dir := filepath.Join(root, "gen")
	mustMkdir(t, dir)
	mustWrite(t, filepath.Join(dir, "real.png"), "x")
	if err := os.Symlink(filepath.Join(dir, "real.png"), filepath.Join(dir, "link.png")); err != nil {
		t.Fatal(err)
	}
	res := peekDir(dir, "gen", time.Now())
	if res.images != 1 || len(res.preview) != 1 || res.preview[0].Name != "real.png" {
		t.Errorf("peek = %+v, want only real.png", res)
	}
}

func TestFSSearchFromSymlinkToHomeHidesDeniedFiles(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed")
	}
	home := hardeningHome(t)
	mustMkdir(t, filepath.Join(home, ".ssh"))
	mustWrite(t, filepath.Join(home, ".ssh", "needle-key"), "k")
	mustWrite(t, filepath.Join(home, "needle-note.txt"), "n")
	if err := os.Symlink(home, filepath.Join(home, "lnk")); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handleFSSearch(rec, httptest.NewRequest("GET", "/fs/search?path=lnk&q=needle", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), ".ssh") {
		t.Errorf("denied file listed: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "needle-note.txt") {
		t.Errorf("ordinary file missing: %s", rec.Body.String())
	}
}

func lineMarksFor(t *testing.T, path string) lineMarksWire {
	t.Helper()
	rec := httptest.NewRecorder()
	handleFSLineMarks(rec, httptest.NewRequest("GET", "/fs/linemarks?path="+path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body.String())
	}
	var out lineMarksWire
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestFSLineMarksUntrackedReadIsSafeAndBounded(t *testing.T) {
	home := hardeningHome(t)
	repo := filepath.Join(home, "repos", "r")
	mustMkdir(t, repo)
	run(t, repo, "git", "init", "-q")
	mustWrite(t, filepath.Join(repo, "ok.txt"), "a\nb\nc")
	mustWrite(t, filepath.Join(repo, "big.txt"), strings.Repeat("x\n", maxEditorFileBytes/2+10))
	if err := syscall.Mkfifo(filepath.Join(repo, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	mustWrite(t, outside, "1\n2\n3\n")
	if err := os.Symlink(outside, filepath.Join(repo, "lnk.txt")); err != nil {
		t.Fatal(err)
	}

	if got := lineMarksFor(t, "repos/r/ok.txt").Added; len(got) != 3 {
		t.Errorf("ordinary untracked file: added = %v, want 3 lines", got)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, p := range []string{"big.txt", "pipe", "lnk.txt"} {
			if got := lineMarksFor(t, "repos/r/"+p).Added; len(got) != 0 {
				t.Errorf("%s: added = %d entries, want none", p, len(got))
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("line marks blocked on a FIFO or an oversized file")
	}
}

func TestCountFileLinesBoundary(t *testing.T) {
	home := hardeningHome(t)
	mustWrite(t, filepath.Join(home, "exact.txt"), strings.Repeat("a", maxEditorFileBytes))
	mustWrite(t, filepath.Join(home, "over.txt"), strings.Repeat("a", maxEditorFileBytes+1))
	mustWrite(t, filepath.Join(home, "nl.txt"), "a\nb\n")
	mustWrite(t, filepath.Join(home, "empty.txt"), "")
	for name, want := range map[string]int{"exact.txt": 1, "over.txt": 0, "nl.txt": 2, "empty.txt": 0, "missing.txt": 0} {
		if got := countFileLines(name); got != want {
			t.Errorf("countFileLines(%s) = %d, want %d", name, got, want)
		}
	}
}

func TestOutDirSinkWritesBeneathAnOrdinaryFolderAndRefusesLinks(t *testing.T) {
	home := hardeningHome(t)
	sink, err := openOutDirSink("gen/new/folder")
	if err != nil {
		t.Fatalf("an ordinary new folder was refused: %v", err)
	}
	if err := sink.WriteFile("a.png", []byte("pixels")); err != nil {
		t.Fatal(err)
	}
	_ = sink.Close()
	b, err := os.ReadFile(filepath.Join(home, "gen", "new", "folder", "a.png"))
	if err != nil || string(b) != "pixels" {
		t.Fatalf("picture not stored: %q %v", b, err)
	}
	if fi, _ := os.Stat(filepath.Join(home, "gen", "new", "folder", "a.png")); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}

	// The folder is replaced by a link after the job was queued: the store-time open refuses.
	if err := os.Symlink(filepath.Join(home, "docs"), filepath.Join(home, "swapped")); err != nil {
		t.Fatal(err)
	}
	if _, err := openOutDirSink("swapped"); err == nil {
		t.Error("a symlinked out_dir was opened for writing")
	}
	if _, _, ok := safeWritableResolvedBrowsePath("swapped/sub"); ok {
		t.Error("a path through a link to an ordinary folder was accepted")
	}
}

// A parent that became a link after the path was checked must not get a sub-folder created
// beyond it, by upload or by an out_dir.
func TestMissingDestinationIsNotCreatedThroughASwappedParent(t *testing.T) {
	home := hardeningHome(t)
	mustMkdir(t, filepath.Join(home, ".ssh"))
	if err := os.Symlink(filepath.Join(home, ".ssh"), filepath.Join(home, "swapped")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(home, "out")); err != nil {
		t.Fatal(err)
	}
	// Straight into the fd-based creation, as if the gate had passed just before the swap.
	for _, rel := range []string{"swapped/new/sub", "out/new/sub"} {
		if d, err := openUploadDir(rel, true); err == nil {
			d.close()
			t.Errorf("%s was opened for creation through a link", rel)
		}
	}
	for _, p := range []string{filepath.Join(home, ".ssh", "new"), filepath.Join(outside, "new")} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was created through a swapped parent", p)
		}
	}
	// Ordinary creation of several missing levels still works.
	d, err := openUploadDir("a/b/c", true)
	if err != nil {
		t.Fatal(err)
	}
	d.close()
	if fi, err := os.Stat(filepath.Join(home, "a", "b", "c")); err != nil || !fi.IsDir() {
		t.Errorf("nested folder not created: %v", err)
	}
}

func TestUploadStoreFailsWhenClosingTheTempFileFails(t *testing.T) {
	home := hardeningHome(t)
	closeTemp = func(f *os.File) error { _ = f.Close(); return syscall.EIO }
	t.Cleanup(func() { closeTemp = func(f *os.File) error { return f.Close() } })

	rec := uploadOnePNG(t, "docs", "late.png")
	if rec.Code == http.StatusOK {
		t.Fatalf("upload reported success after a close failure (%s)", rec.Body.String())
	}
	ents, _ := os.ReadDir(filepath.Join(home, "docs"))
	for _, e := range ents {
		if e.Name() != "a.txt" {
			t.Errorf("left behind: %s", e.Name())
		}
	}
}
