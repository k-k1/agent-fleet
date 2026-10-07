package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// zipEnv points the browse root and the agent state dir at a temp home, and restores every
// limit the tests below shrink.
func zipEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_BROWSE_ROOT", home)
	t.Setenv("AGENT_DOCS_DIR", filepath.Join(t.TempDir(), "docs"))
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	files, dirs, depth, names, looked, bytesCap, build, batch, wait := zipMaxFiles, zipMaxDirs, zipMaxDepth, zipMaxNameBytes, zipMaxLooked, zipMaxBytes, zipBuildLimit, zipReadBatch, zipSlotWait
	t.Cleanup(func() {
		zipMaxFiles, zipMaxDirs, zipMaxDepth, zipMaxNameBytes, zipMaxLooked, zipMaxBytes, zipBuildLimit, zipReadBatch, zipSlotWait =
			files, dirs, depth, names, looked, bytesCap, build, batch, wait
		zipAfterPlan = nil
	})
	return home
}

func zipPut(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func zipGet(t *testing.T, path string, extra ...string) *httptest.ResponseRecorder {
	t.Helper()
	q := url.Values{"path": {path}}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	rec := httptest.NewRecorder()
	handleFSDownloadZip(rec, httptest.NewRequest("GET", "/fs/download-zip?"+q.Encode(), nil))
	return rec
}

// zipContents unpacks a response into name -> content ("" for a folder entry).
func zipContents(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		if _, dup := out[f.Name]; dup {
			t.Fatalf("duplicate entry %q", f.Name)
		}
		out[f.Name] = string(b)
	}
	return out
}

func zipErrCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("status %d, body is not an error envelope: %q", rec.Code, rec.Body.String())
	}
	return env.Error.Code
}

func wantZipErr(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status || zipErrCode(t, rec) != code {
		t.Fatalf("got %d %q, want %d %q", rec.Code, rec.Body.String(), status, code)
	}
	if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "zip") {
		t.Fatalf("an error must not look like an archive: %q", ct)
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestZipFolderRoundTrip(t *testing.T) {
	home := zipEnv(t)
	zipPut(t, home, "proj/a.txt", "alpha")
	zipPut(t, home, "proj/sub/日本語 ファイル.md", "unicode")
	zipPut(t, home, "proj/sub/.hidden", "dot")
	zipPut(t, home, "proj/sub/deeper/b.bin", "\x00\x01\x02")
	if err := os.MkdirAll(filepath.Join(home, "proj", "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(home, "proj", "a.txt"), 0o755); err != nil {
		t.Fatal(err)
	}

	rec := zipGet(t, "proj")
	got := zipContents(t, rec)
	want := map[string]string{
		"proj/": "", "proj/a.txt": "alpha", "proj/sub/": "", "proj/sub/日本語 ファイル.md": "unicode",
		"proj/sub/.hidden": "dot", "proj/sub/deeper/": "", "proj/sub/deeper/b.bin": "\x00\x01\x02", "proj/empty/": "",
	}
	if strings.Join(sortedKeys(got), "|") != strings.Join(sortedKeys(want), "|") {
		t.Fatalf("entries = %q, want %q", sortedKeys(got), sortedKeys(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %q, want %q", k, got[k], v)
		}
	}
	h := rec.Header()
	if h.Get("Content-Type") != "application/zip" {
		t.Fatalf("Content-Type = %q", h.Get("Content-Type"))
	}
	if h.Get("Content-Length") == "" || h.Get("Content-Length") != strconv.Itoa(rec.Body.Len()) {
		t.Fatalf("Content-Length = %q for %d bytes", h.Get("Content-Length"), rec.Body.Len())
	}
	if cd := h.Get("Content-Disposition"); !strings.Contains(cd, `filename="proj.zip"`) || !strings.Contains(cd, "filename*=UTF-8''proj.zip") {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	if h.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("Cache-Control = %q", h.Get("Cache-Control"))
	}
	// Stored, not deflated: images and archives do not recompress, and no compression workers.
	zr, _ := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	for _, f := range zr.File {
		if f.Method != zip.Store {
			t.Fatalf("%s method = %d, want Store", f.Name, f.Method)
		}
		if f.Name == "proj/a.txt" && f.Mode().Perm() != 0o755 {
			t.Fatalf("mode lost: %v", f.Mode())
		}
	}
	if _, err := os.Stat(zipTempDir()); err == nil {
		ents, _ := os.ReadDir(zipTempDir())
		if len(ents) != 0 {
			t.Fatalf("temp files left behind: %v", ents)
		}
	}
}

func TestZipEmptyFolder(t *testing.T) {
	home := zipEnv(t)
	if err := os.MkdirAll(filepath.Join(home, "nothing"), 0o700); err != nil {
		t.Fatal(err)
	}
	got := zipContents(t, zipGet(t, "nothing"))
	if len(got) != 1 {
		t.Fatalf("entries = %v, want the folder alone", got)
	}
	if _, ok := got["nothing/"]; !ok {
		t.Fatalf("no top-level folder entry: %v", got)
	}
}

func TestZipExcludesGitAndNodeModulesBelowStartOnly(t *testing.T) {
	home := zipEnv(t)
	zipPut(t, home, "repo/src/a.go", "a")
	zipPut(t, home, "repo/.git/HEAD", "ref")
	zipPut(t, home, "repo/web/node_modules/x/index.js", "x")
	zipPut(t, home, "repo/web/app.js", "app")
	got := zipContents(t, zipGet(t, "repo"))
	for name := range got {
		if strings.Contains(name, ".git") || strings.Contains(name, "node_modules") {
			t.Fatalf("%q must be left out", name)
		}
	}
	if got["repo/web/app.js"] != "app" || got["repo/src/a.go"] != "a" {
		t.Fatalf("ordinary files missing: %v", sortedKeys(got))
	}

	// The check says what was left out, so the Console can tell the user before the download.
	rec := zipGet(t, "repo", "check", "1")
	var chk fsZipCheck
	if err := json.Unmarshal(rec.Body.Bytes(), &chk); err != nil {
		t.Fatal(err)
	}
	if strings.Join(chk.Excluded, ",") != ".git,node_modules" || chk.Files != 2 || chk.Name != "repo.zip" {
		t.Fatalf("check = %+v", chk)
	}

	// A start folder that IS one of them is what the user asked for: exported, not excluded.
	got = zipContents(t, zipGet(t, "repo/web/node_modules"))
	if got["node_modules/x/index.js"] != "x" {
		t.Fatalf("selected node_modules not exported: %v", sortedKeys(got))
	}
	got = zipContents(t, zipGet(t, "repo/.git"))
	if got[".git/HEAD"] != "ref" {
		t.Fatalf("selected .git not exported: %v", sortedKeys(got))
	}
}

func TestZipCheckDoesNotBuildAnArchive(t *testing.T) {
	home := zipEnv(t)
	zipPut(t, home, "p/a", "12345")
	zipPut(t, home, "p/b/c", "123")
	rec := zipGet(t, "p", "check", "1")
	if rec.Code != 200 || rec.Header().Get("Content-Type") == "application/zip" {
		t.Fatalf("check answered %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var chk fsZipCheck
	_ = json.Unmarshal(rec.Body.Bytes(), &chk)
	if chk.Files != 2 || chk.Dirs != 2 || chk.Bytes != 8 {
		t.Fatalf("check = %+v", chk)
	}
	if _, err := os.Stat(zipTempDir()); err == nil {
		t.Fatal("a check must not touch the temp dir")
	}
}

func TestZipDenylistAndGeneratedImagesNotInherited(t *testing.T) {
	home := zipEnv(t)
	zipPut(t, home, "proj/ok.txt", "ok")
	zipPut(t, home, ".ssh/id_rsa", "SECRET")
	zipPut(t, home, ".codex/generated_images/s1/a.png", "PNG")
	zipPut(t, home, ".codex/auth.json", "TOKEN")
	zipPut(t, home, ".local/share/agent-fleet/store", "STORE")
	zipPut(t, home, ".local/share/other/keep.txt", "keep")

	// A denied start folder, directly or from below.
	wantZipErr(t, zipGet(t, ".ssh"), 403, "denied")
	wantZipErr(t, zipGet(t, ".codex"), 403, "denied")
	// The single-image exception of /fs/download is not carried over to the bulk export.
	wantZipErr(t, zipGet(t, ".codex/generated_images"), 403, "denied")
	wantZipErr(t, zipGet(t, ".codex/generated_images/s1"), 403, "denied")
	wantZipErr(t, zipGet(t, filepath.Join(home, ".codex/generated_images/s1")), 403, "denied")
}

func TestZipParentOfDeniedFoldersPrunesThem(t *testing.T) {
	home := zipEnv(t)
	zipPut(t, home, "wrap/ok.txt", "ok")
	zipPut(t, home, "wrap/.ssh/id_rsa", "SECRET")
	zipPut(t, home, ".local/share/agent-fleet/store", "STORE")
	zipPut(t, home, ".local/share/other/keep.txt", "keep")
	if err := os.Symlink(filepath.Join(home, ".ssh"), filepath.Join(home, "wrap", "link")); err != nil {
		t.Fatal(err)
	}
	got := zipContents(t, zipGet(t, ".local"))
	for name := range got {
		if strings.Contains(name, "agent-fleet") || strings.Contains(name, "STORE") {
			t.Fatalf("denied tree leaked: %q", name)
		}
	}
	if got[".local/share/other/keep.txt"] != "keep" {
		t.Fatalf("sibling missing: %v", sortedKeys(got))
	}
	// wrap/.ssh is not denied by the browse-relative list (only the root's .ssh is), but the
	// symlink inside must never be followed.
	for name, body := range zipContents(t, zipGet(t, "wrap")) {
		if strings.Contains(body, "SECRET") && name != "wrap/.ssh/id_rsa" {
			t.Fatalf("symlink followed into %q", name)
		}
	}
}

func TestZipSymlinks(t *testing.T) {
	home := zipEnv(t)
	outside := t.TempDir()
	zipPut(t, outside, "secret.txt", "OUTSIDE")
	zipPut(t, home, "real/inner.txt", "inner")
	zipPut(t, home, "tree/a.txt", "a")
	for link, target := range map[string]string{
		"alias":                 filepath.Join(home, "real"), // the START path is a symlink
		"tree/file-link":        filepath.Join(outside, "secret.txt"),
		"tree/dir-link":         outside,
		"tree/relative-escape":  "../../../../../../etc/passwd",
		"tree/inside-file-link": "a.txt",
		"viaparent":             filepath.Join(home, "tree"),
		"tree/dangling":         filepath.Join(home, "nowhere"),
	} {
		if err := os.Symlink(target, filepath.Join(home, link)); err != nil {
			t.Fatal(err)
		}
	}
	// Start path is a symlink, or has one in the middle: refused, not followed.
	wantZipErr(t, zipGet(t, "alias"), 400, "symlink_not_allowed")
	wantZipErr(t, zipGet(t, "viaparent/a.txt"), 400, "symlink_not_allowed")
	if err := os.MkdirAll(filepath.Join(home, "tree", "deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	wantZipErr(t, zipGet(t, "viaparent/deep"), 400, "symlink_not_allowed")

	// Links inside the folder are left out and counted, never followed.
	rec := zipGet(t, "tree")
	got := zipContents(t, rec)
	for name, body := range got {
		if strings.Contains(body, "OUTSIDE") || strings.Contains(name, "link") || strings.Contains(name, "dangling") || strings.Contains(name, "escape") {
			t.Fatalf("symlink leaked: %q", name)
		}
	}
	if got["tree/a.txt"] != "a" {
		t.Fatalf("regular file missing: %v", sortedKeys(got))
	}
	var chk fsZipCheck
	_ = json.Unmarshal(zipGet(t, "tree", "check", "1").Body.Bytes(), &chk)
	if chk.Skipped != 5 {
		t.Fatalf("skipped = %d, want 5 links", chk.Skipped)
	}
}

func TestZipSwapAfterWalkFailsInsteadOfReadingThroughALink(t *testing.T) {
	home := zipEnv(t)
	outside := t.TempDir()
	zipPut(t, outside, "secret.txt", "OUTSIDE")

	swaps := map[string]func(dir string){
		"file becomes a symlink": func(dir string) {
			p := filepath.Join(dir, "a.txt")
			_ = os.Remove(p)
			_ = os.Symlink(filepath.Join(outside, "secret.txt"), p)
		},
		"folder becomes a symlink": func(dir string) {
			p := filepath.Join(dir, "sub")
			_ = os.RemoveAll(p)
			_ = os.Symlink(outside, p)
		},
		"file vanishes": func(dir string) { _ = os.Remove(filepath.Join(dir, "a.txt")) },
		"file becomes a fifo": func(dir string) {
			p := filepath.Join(dir, "a.txt")
			_ = os.Remove(p)
			_ = unix.Mkfifo(p, 0o600)
		},
		"file becomes a folder": func(dir string) {
			p := filepath.Join(dir, "a.txt")
			_ = os.Remove(p)
			_ = os.Mkdir(p, 0o700)
		},
	}
	n := 0
	for name, swap := range swaps {
		n++
		rel := "proj" + strconv.Itoa(n)
		t.Run(name, func(t *testing.T) {
			zipPut(t, home, rel+"/a.txt", "a")
			zipPut(t, home, rel+"/sub/b.txt", "b")
			zipAfterPlan = func() { swap(filepath.Join(home, rel)) }
			defer func() { zipAfterPlan = nil }()
			rec := zipGet(t, rel)
			wantZipErr(t, rec, 409, "changed_during_zip")
			if strings.Contains(rec.Body.String(), "OUTSIDE") {
				t.Fatal("read through the swapped link")
			}
		})
	}
}

func TestZipSpecialFilesAreSkippedAndCounted(t *testing.T) {
	home := zipEnv(t)
	zipPut(t, home, "d/a.txt", "a")
	if err := unix.Mkfifo(filepath.Join(home, "d", "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- zipGet(t, "d") }()
	select {
	case rec := <-done:
		got := zipContents(t, rec)
		if _, ok := got["d/pipe"]; ok || got["d/a.txt"] != "a" {
			t.Fatalf("entries = %v", sortedKeys(got))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a FIFO in the folder blocked the walk")
	}
	var chk fsZipCheck
	_ = json.Unmarshal(zipGet(t, "d", "check", "1").Body.Bytes(), &chk)
	if chk.Skipped != 1 {
		t.Fatalf("skipped = %d", chk.Skipped)
	}
}

func TestZipHostileNamesFailTheExport(t *testing.T) {
	home := zipEnv(t)
	for name, file := range map[string]string{
		"backslash": "..\\..\\evil",
		"newline":   "a\nb",
		"control":   "a\x01b",
		"badutf8":   "a\xffb",
		"drive-ish": "C:",
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(home, "h-"+name)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			zipPut(t, dir, "fine.txt", "fine")
			if err := os.WriteFile(filepath.Join(dir, file), []byte("x"), 0o600); err != nil {
				t.Skipf("filesystem refuses this name: %v", err)
			}
			rec := zipGet(t, "h-"+name)
			if name == "drive-ish" {
				// "C:" below the folder is "h-drive-ish/C:" — not a prefix, a legal Unix name.
				zipContents(t, rec)
				return
			}
			wantZipErr(t, rec, 422, "unsafe_name")
		})
	}
}

func TestZipNameProblem(t *testing.T) {
	bad := []string{"", "/abs", "a\\b", "a/../b", "a/./b", "a//b", "../x", "C:/x", "c:x", "a/b\x00", "a\tb", "a\x7fb", "a\xffb", "..", "."}
	for _, n := range bad {
		if zipNameProblem(n) == "" {
			t.Errorf("%q must be refused", n)
		}
	}
	good := []string{"a", "a/b", "日本語/ファイル.txt", "a b/c d", "dir/", "a/C:", "ab:c"}
	for _, n := range good {
		if p := zipNameProblem(n); p != "" {
			t.Errorf("%q refused: %s", n, p)
		}
	}
}

func TestZipPathValidation(t *testing.T) {
	home := zipEnv(t)
	zipPut(t, home, "d/a", "a")
	for _, q := range []string{"", "..", "../x", "d/..", "d/../d", "./d", "d/", "d//x", `d\x`, "C:\\x", "d\x00"} {
		if rec := zipGet(t, q); rec.Code != 400 {
			t.Errorf("path %q -> %d, want 400", q, rec.Code)
		}
	}
	// The roots themselves are not exportable; a folder inside them is.
	wantZipErr(t, zipGet(t, home), 400, "bad_path")
	wantZipErr(t, zipGet(t, "/etc"), 400, "bad_path")
	wantZipErr(t, zipGet(t, "nope"), 404, "zip_not_dir")
	wantZipErr(t, zipGet(t, "d/a"), 404, "zip_not_dir") // a file
	got := zipContents(t, zipGet(t, filepath.Join(home, "d")))
	if got["d/a"] != "a" {
		t.Fatalf("absolute path under the browse root: %v", sortedKeys(got))
	}
}

func TestZipScratchAndDocsRoots(t *testing.T) {
	home := zipEnv(t)
	_ = home
	scratch := scratchRoot()
	docs := agentFleetDocsRoot()
	zipPut(t, scratch, "session/out.txt", "scratch")
	zipPut(t, docs, "guide/a.md", "doc")
	if got := zipContents(t, zipGet(t, filepath.Join(scratch, "session"))); got["session/out.txt"] != "scratch" {
		t.Fatalf("scratch: %v", sortedKeys(got))
	}
	if got := zipContents(t, zipGet(t, filepath.Join(docs, "guide"))); got["guide/a.md"] != "doc" {
		t.Fatalf("docs: %v", sortedKeys(got))
	}
	wantZipErr(t, zipGet(t, scratch), 400, "bad_path")
	wantZipErr(t, zipGet(t, docs), 400, "bad_path")
}

func TestZipCapBoundaries(t *testing.T) {
	home := zipEnv(t)
	for i := 0; i < 5; i++ {
		zipPut(t, home, "f/"+string(rune('a'+i)), "1234")
	}
	zipPut(t, home, "f/s1/s2/s3/leaf", "x")

	t.Run("files", func(t *testing.T) {
		zipMaxFiles = 6
		zipContents(t, zipGet(t, "f"))
		zipMaxFiles = 5
		wantZipErr(t, zipGet(t, "f"), 413, "zip_too_large")
		wantZipErr(t, zipGet(t, "f", "check", "1"), 413, "zip_too_large")
	})
	t.Run("dirs", func(t *testing.T) {
		zipEnvLimitsReset()
		zipMaxDirs = 4 // the start itself counts: f, s1, s2, s3
		zipContents(t, zipGet(t, "f"))
		zipMaxDirs = 3
		wantZipErr(t, zipGet(t, "f"), 413, "zip_too_large")
	})
	t.Run("depth", func(t *testing.T) {
		zipEnvLimitsReset()
		zipMaxDepth = 3
		zipContents(t, zipGet(t, "f"))
		zipMaxDepth = 2
		wantZipErr(t, zipGet(t, "f"), 413, "zip_too_large")
	})
	t.Run("bytes", func(t *testing.T) {
		zipEnvLimitsReset()
		zipMaxBytes = 21 // 5*4 + 1
		zipContents(t, zipGet(t, "f"))
		zipMaxBytes = 20
		wantZipErr(t, zipGet(t, "f"), 413, "zip_too_large")
	})
	t.Run("names", func(t *testing.T) {
		zipEnvLimitsReset()
		total := len("f") + len("f/s1") + len("f/s1/s2") + len("f/s1/s2/s3") + len("f/s1/s2/s3/leaf")
		for _, n := range []string{"a", "b", "c", "d", "e"} {
			total += len("f/" + n)
		}
		zipMaxNameBytes = total
		zipContents(t, zipGet(t, "f"))
		zipMaxNameBytes = total - 1
		wantZipErr(t, zipGet(t, "f"), 413, "zip_too_large")
	})
	t.Run("looked", func(t *testing.T) {
		zipEnvLimitsReset()
		if err := os.Symlink("a", filepath.Join(home, "f", "l1")); err != nil {
			t.Fatal(err)
		}
		zipMaxLooked = 10 // 5 files + s1 + l1 + s2 + s3 + leaf
		zipContents(t, zipGet(t, "f"))
		zipMaxLooked = 9
		wantZipErr(t, zipGet(t, "f"), 413, "zip_too_large")
		_ = os.Remove(filepath.Join(home, "f", "l1"))
	})
	t.Run("time", func(t *testing.T) {
		zipEnvLimitsReset()
		zipBuildLimit = time.Nanosecond
		wantZipErr(t, zipGet(t, "f"), 413, "zip_too_large")
	})
	t.Run("batching", func(t *testing.T) {
		zipEnvLimitsReset()
		zipReadBatch = 2
		got := zipContents(t, zipGet(t, "f"))
		if len(got) != 10 {
			t.Fatalf("entries = %v", sortedKeys(got))
		}
	})
}

var zipDefaults = struct {
	files, dirs, depth, names, looked int
	bytes                             int64
	build                             time.Duration
	batch                             int
}{zipMaxFiles, zipMaxDirs, zipMaxDepth, zipMaxNameBytes, zipMaxLooked, zipMaxBytes, zipBuildLimit, zipReadBatch}

func zipEnvLimitsReset() {
	zipMaxFiles, zipMaxDirs, zipMaxDepth, zipMaxNameBytes, zipMaxLooked, zipMaxBytes, zipBuildLimit, zipReadBatch =
		zipDefaults.files, zipDefaults.dirs, zipDefaults.depth, zipDefaults.names, zipDefaults.looked, zipDefaults.bytes, zipDefaults.build, zipDefaults.batch
}

// Sizes are listing-time values. A file that grows after the walk is stopped by the bytes
// actually read, not by what the listing said.
func TestZipGrowthAfterWalkIsCaughtByBytesRead(t *testing.T) {
	home := zipEnv(t)
	zipPut(t, home, "g/a", "1234")
	zipPut(t, home, "g/b", "1234")
	zipMaxBytes = 10
	zipAfterPlan = func() { zipPut(t, home, "g/a", strings.Repeat("x", 64)) }
	wantZipErr(t, zipGet(t, "g"), 413, "zip_too_large")
	if ents, _ := os.ReadDir(zipTempDir()); len(ents) != 0 {
		t.Fatalf("temp files left: %v", ents)
	}
}

func TestZipShrinkAfterWalkPacksWhatWasRead(t *testing.T) {
	home := zipEnv(t)
	zipPut(t, home, "s/a", "123456789")
	zipAfterPlan = func() { zipPut(t, home, "s/a", "12") }
	got := zipContents(t, zipGet(t, "s"))
	if got["s/a"] != "12" {
		t.Fatalf("a = %q", got["s/a"])
	}
}

func TestZipUnreadableFileFailsTheExport(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads anything")
	}
	home := zipEnv(t)
	zipPut(t, home, "u/ok", "ok")
	zipPut(t, home, "u/secret", "no")
	if err := os.Chmod(filepath.Join(home, "u", "secret"), 0); err != nil {
		t.Fatal(err)
	}
	wantZipErr(t, zipGet(t, "u"), 403, "denied")
}

func TestZipSlotHeldForTheWholeRequestAndReleased(t *testing.T) {
	home := zipEnv(t)
	zipPut(t, home, "x/a", "a")
	zipSlotWait = 50 * time.Millisecond

	// Held while a build is mid-flight: a second request is refused with 503, not queued.
	inside := make(chan struct{})
	release := make(chan struct{})
	zipAfterPlan = func() {
		close(inside)
		<-release
	}
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- zipGet(t, "x") }()
	<-inside
	zipAfterPlan = nil
	wantZipErr(t, zipGet(t, "x"), 503, "zip_busy")
	close(release)
	zipContents(t, <-first)

	// Released after success, after a refusal, and after the client went away.
	zipContents(t, zipGet(t, "x"))
	zipMaxFiles = 0
	wantZipErr(t, zipGet(t, "x"), 413, "zip_too_large")
	zipMaxFiles = 20000
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	handleFSDownloadZip(rec, httptest.NewRequest("GET", "/fs/download-zip?path=x", nil).WithContext(ctx))
	if rec.Body.Len() != 0 {
		t.Fatalf("answered a request nobody is waiting for: %q", rec.Body.String())
	}
	if len(zipSlots) != 0 {
		t.Fatal("the slot was not released")
	}
	zipContents(t, zipGet(t, "x"))
}

func TestZipBusySlotWaitsForARequestThatCancels(t *testing.T) {
	home := zipEnv(t)
	zipPut(t, home, "x/a", "a")
	zipSlots <- struct{}{}
	defer func() { <-zipSlots }()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	rec := httptest.NewRecorder()
	go func() {
		defer wg.Done()
		handleFSDownloadZip(rec, httptest.NewRequest("GET", "/fs/download-zip?path=x", nil).WithContext(ctx))
	}()
	cancel()
	wg.Wait()
	if rec.Body.Len() != 0 {
		t.Fatalf("body %q", rec.Body.String())
	}
}

func TestZipStaleTempFilesAreSweptAndTheOneInUseIsUnlinked(t *testing.T) {
	home := zipEnv(t)
	zipPut(t, home, "x/a", "a")
	dir := zipTempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "zip-crashed.tmp")
	if err := os.WriteFile(stale, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	zipContents(t, zipGet(t, "x"))
	if _, err := os.Stat(stale); err == nil {
		t.Fatal("a crashed build's temp file was not swept")
	}
	f, err := newZipTemp()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	during, _ := os.ReadDir(dir)
	if len(during) != 0 {
		t.Fatalf("the temp file in use is still linked: %v", during)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("temp dir mode = %v", fi.Mode().Perm())
	}
}

func TestZipHeadAndPostAreRefused(t *testing.T) {
	home := zipEnv(t)
	zipPut(t, home, "x/a", "a")
	for _, m := range []string{"HEAD", "POST"} {
		rec := httptest.NewRecorder()
		handleFSDownloadZip(rec, httptest.NewRequest(m, "/fs/download-zip?path=x", nil))
		if rec.Code != 405 {
			t.Fatalf("%s -> %d", m, rec.Code)
		}
	}
	if ents, _ := os.ReadDir(zipTempDir()); len(ents) != 0 {
		t.Fatalf("a refused request built something: %v", ents)
	}
}

func TestZipRangeIsNotHonoured(t *testing.T) {
	home := zipEnv(t)
	zipPut(t, home, "x/a", "aaaa")
	req := httptest.NewRequest("GET", "/fs/download-zip?path=x", nil)
	req.Header.Set("Range", "bytes=10-")
	req.Header.Set("If-Range", `"anything"`)
	rec := httptest.NewRecorder()
	handleFSDownloadZip(rec, req)
	if rec.Code != 200 {
		t.Fatalf("ranged request -> %d; each build is fresh, a range of one is not a range of the next", rec.Code)
	}
	zipContents(t, rec)
}

func TestZipDisposition(t *testing.T) {
	cases := map[string][2]string{
		"proj":        {`filename="proj.zip"`, "filename*=UTF-8''proj.zip"},
		"日本語 dir":     {`filename="___ dir.zip"`, "filename*=UTF-8''%E6%97%A5%E6%9C%AC%E8%AA%9E%20dir.zip"},
		`a"b\c%d`:     {`filename="a_b_c_d.zip"`, "filename*=UTF-8''a%22b%5Cc%25d.zip"},
		"line\nbreak": {`filename="line_break.zip"`, "filename*=UTF-8''line%0Abreak.zip"},
		"":            {`filename="folder.zip"`, "filename*=UTF-8''folder.zip"},
	}
	for in, want := range cases {
		got := zipDisposition(in)
		if !strings.HasPrefix(got, "attachment; ") || !strings.Contains(got, want[0]) || !strings.Contains(got, want[1]) {
			t.Errorf("zipDisposition(%q) = %q, want %q and %q", in, got, want[0], want[1])
		}
		if strings.ContainsAny(got, "\r\n") {
			t.Errorf("header injection in %q", got)
		}
	}
}

// Through the real middleware stack: the gzip wrapper must leave an archive alone and still
// let the zip handler reach the connection (Unwrap) to set its transfer deadline.
func TestZipThroughTheServerStack(t *testing.T) {
	home := zipEnv(t)
	zipPut(t, home, "srv/big.bin", strings.Repeat("0123456789", 5000))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fs/download-zip", handleFSDownloadZip)
	srv := httptest.NewServer(httpx.Gzip(mux))
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/fs/download-zip?path=srv", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Content-Encoding") != "" || resp.ContentLength != int64(len(body)) {
		t.Fatalf("encoding %q, Content-Length %d, body %d", resp.Header.Get("Content-Encoding"), resp.ContentLength, len(body))
	}
	if _, err := zip.NewReader(bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatal(err)
	}
}
