package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// isolateToolCaches points every cache location and /proc at temp dirs. Without it a
// GOCACHE (or npm_config_cache …) set on the machine running the test would make a DELETE
// here empty the real cache, and the `go` running this very test would read as busy.
func isolateToolCaches(t *testing.T) (home, proc string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	for _, c := range toolCaches {
		if c.env != "" {
			t.Setenv(c.env, "")
		}
	}
	proc = t.TempDir()
	old := procRoot
	procRoot = proc
	t.Cleanup(func() { procRoot = old })
	toolCacheUsageCache.mu.Lock()
	toolCacheUsageCache.val = nil
	toolCacheUsageCache.mu.Unlock()
	return home, proc
}

func fakeProc(t *testing.T, proc, pid string, argv ...string) {
	t.Helper()
	dir := filepath.Join(proc, pid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeSized(t *testing.T, p string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestToolCacheUsersMatchTheRealArgvShapes(t *testing.T) {
	cases := []struct {
		cache string
		argv  []string
		want  bool
	}{
		{"go-build", []string{"/usr/local/go/bin/go", "build", "./..."}, true},
		{"go-build", []string{"gopls"}, false},
		{"npm", []string{"node", "/home/dev/.nvm/versions/node/v22/bin/npm", "ci"}, true},
		{"npm", []string{"node", "/usr/lib/node_modules/npm/bin/npm-cli.js", "install"}, true},
		{"npm", []string{"npx", "vitest"}, true},
		{"npm", []string{"node", "server.js"}, false},
		{"uv", []string{"/home/dev/.local/bin/uv", "sync"}, true},
		{"maven", []string{"/usr/bin/mvn", "package"}, true},
		{"maven", []string{"sh", "./mvnw", "verify"}, true},
		{"maven", []string{"/bin/bash", "/home/dev/repos/x/mvnw", "test"}, true},
		{"maven", []string{"/usr/lib/jvm/java-21/bin/java", "-classpath", "/m/boot/plexus-classworlds.jar", "-Dmaven.multiModuleProjectDirectory=/x", "org.codehaus.plexus.classworlds.launcher.Launcher", "test"}, true},
		{"maven", []string{"java", "-jar", "app.jar"}, false},
		{"maven", []string{"bash", "build.sh", "mvnw-notes"}, false},
		{"npx", []string{"node", "/usr/lib/node_modules/npm/bin/npx-cli.js", "cowsay"}, true},
		{"npx", []string{"node", "server.js"}, false},
		{"npx", []string{"node", "/usr/lib/node_modules/npm/bin/npm-cli.js", "exec", "cowsay"}, true},
		{"npx", []string{"node", "/usr/lib/node_modules/npm/bin/npm-cli.js", "--yes", "x", "cowsay"}, true},
		{"npx", []string{"npm", "exec", "--", "cowsay"}, true},
		{"npx", []string{"npm", "--cache", "/tmp/c", "exec", "cowsay"}, true},
		{"npx", []string{"node", "/usr/lib/node_modules/npm/bin/npm-cli.js", "--prefix", "/p", "exec", "x"}, true},
		{"maven", []string{"bash", "-c", "x -Dmaven.multiModuleProjectDirectory=/x"}, false},
		{"maven", []string{"tail", "-f", "org.codehaus.plexus.classworlds.launcher.Launcher"}, false},
		{"pip", []string{"python3", "-m", "pip", "install", "x"}, true},
		{"pip", []string{"python3", "-m", "http.server"}, false},
	}
	for _, tc := range cases {
		c, _ := findToolCache(tc.cache)
		if got := c.users(tc.argv); got != tc.want {
			t.Errorf("%s users(%q) = %v, want %v", tc.cache, tc.argv, got, tc.want)
		}
	}
}

func TestToolCacheDirFollowsTheToolsVariable(t *testing.T) {
	home, _ := isolateToolCaches(t)
	npm, _ := findToolCache("npm")
	if got, want := npm.dir(), filepath.Join(home, ".npm/_cacache"); got != want {
		t.Fatalf("default npm dir = %s, want %s", got, want)
	}
	moved := t.TempDir()
	t.Setenv("npm_config_cache", moved)
	if got, want := npm.dir(), filepath.Join(moved, "_cacache"); got != want {
		t.Fatalf("moved npm dir = %s, want %s", got, want)
	}
}

func TestToolCacheUsageListsOnlyPresentCachesWithBusy(t *testing.T) {
	home, proc := isolateToolCaches(t)
	writeSized(t, filepath.Join(home, ".cache/go-build/ab/x-d"), 100)
	writeSized(t, filepath.Join(home, ".cache/go-build/cd/y-d"), 50)
	fakeProc(t, proc, "4242", "/usr/local/go/bin/go", "test", "./...")

	rec := httptest.NewRecorder()
	handleToolCacheUsage(rec, httptest.NewRequest("GET", "/cleanup/tool-caches", nil))
	var u toolCacheUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}
	if len(u.Caches) != 1 || u.Caches[0].Name != "go-build" {
		t.Fatalf("caches = %+v, want only go-build", u.Caches)
	}
	got := u.Caches[0]
	if got.Bytes != 150 || got.Files != 2 || len(got.Busy) != 1 || got.Busy[0] != 4242 || got.Path != "~/.cache/go-build" {
		t.Fatalf("row = %+v", got)
	}
}

func TestDeleteToolCacheRefusesWhileInUse(t *testing.T) {
	home, proc := isolateToolCaches(t)
	f := filepath.Join(home, ".npm/_cacache/content-v2/sha512/aa")
	writeSized(t, f, 10)
	fakeProc(t, proc, "77", "node", "/x/bin/npm-cli.js", "ci")

	req := httptest.NewRequest("DELETE", "/cleanup/tool-caches/npm", nil)
	req.SetPathValue("name", "npm")
	rec := httptest.NewRecorder()
	handleDeleteToolCache(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body)
	}
	if _, err := os.Stat(f); err != nil {
		t.Fatalf("cache touched while in use: %v", err)
	}
}

func TestDeleteToolCacheEmptiesThroughSymlinkAndKeepsTheLink(t *testing.T) {
	home, _ := isolateToolCaches(t)
	// $AF_WS_SCRATCH: the cache dir is a symlink into the task disk.
	scratch := t.TempDir()
	writeSized(t, filepath.Join(scratch, "ab", "x-d"), 30)
	// A read-only directory, as Go's module cache leaves them.
	writeSized(t, filepath.Join(scratch, "ro", "f"), 5)
	if err := os.Chmod(filepath.Join(scratch, "ro"), 0o500); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".cache/go-build")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(scratch, link); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("DELETE", "/cleanup/tool-caches/go-build", nil)
	req.SetPathValue("name", "go-build")
	rec := httptest.NewRecorder()
	handleDeleteToolCache(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink not kept: %v %v", fi, err)
	}
	if ents, _ := os.ReadDir(scratch); len(ents) != 0 {
		t.Fatalf("scratch not emptied: %v", ents)
	}
	var res struct{ Bytes int64 }
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Bytes != 35 {
		t.Fatalf("bytes = %d, want 35", res.Bytes)
	}
}

func TestDeleteToolCacheRejectsUnknownName(t *testing.T) {
	isolateToolCaches(t)
	req := httptest.NewRequest("DELETE", "/cleanup/tool-caches/..", nil)
	req.SetPathValue("name", "..")
	rec := httptest.NewRecorder()
	handleDeleteToolCache(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestSessionWorkDirGoesWithTheSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := paths.SessionWorkDir("sabc123")
	if wd != filepath.Join(home, ".af-work", "sabc123") {
		t.Fatalf("work dir = %s", wd)
	}
	writeSized(t, filepath.Join(wd, "probe", "out.txt"), 3)
	other := paths.SessionWorkDir("sother1")
	writeSized(t, filepath.Join(other, "keep"), 1)

	removeSessionSideFiles("sabc123")
	if _, err := os.Stat(wd); !os.IsNotExist(err) {
		t.Fatalf("work dir survived delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "keep")); err != nil {
		t.Fatalf("another session's work dir was touched: %v", err)
	}
	for _, bad := range []string{"", ".", "..", "a/b"} {
		if got := paths.SessionWorkDir(bad); got != "" {
			t.Errorf("SessionWorkDir(%q) = %q, want \"\"", bad, got)
		}
	}
}

func TestMavenAndNpxCachesEmptyOnlyTheirOwnDirectory(t *testing.T) {
	home, _ := isolateToolCaches(t)
	settings := filepath.Join(home, ".m2/settings.xml")
	writeSized(t, settings, 5)
	writeSized(t, filepath.Join(home, ".m2/repository/org/x/x.jar"), 100)
	writeSized(t, filepath.Join(home, ".npm/_npx/abc/package.json"), 7)
	cacache := filepath.Join(home, ".npm/_cacache/k")
	writeSized(t, cacache, 9)

	for _, name := range []string{"maven", "npx"} {
		req := httptest.NewRequest("DELETE", "/cleanup/tool-caches/"+name, nil)
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		handleDeleteToolCache(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d (%s)", name, rec.Code, rec.Body)
		}
	}
	for _, p := range []string{settings, cacache, filepath.Join(home, ".m2/repository"), filepath.Join(home, ".npm/_npx")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s should survive: %v", p, err)
		}
	}
	for _, p := range []string{filepath.Join(home, ".m2/repository/org"), filepath.Join(home, ".npm/_npx/abc")} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s should be gone", p)
		}
	}
}

func TestMavenCacheMissingIsAbsentNotAnError(t *testing.T) {
	isolateToolCaches(t)
	req := httptest.NewRequest("DELETE", "/cleanup/tool-caches/maven", nil)
	req.SetPathValue("name", "maven")
	rec := httptest.NewRecorder()
	handleDeleteToolCache(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	handleToolCacheUsage(rec, httptest.NewRequest("GET", "/cleanup/tool-caches", nil))
	if strings.Contains(rec.Body.String(), `"maven"`) {
		t.Fatalf("absent maven repo listed: %s", rec.Body)
	}
}

func TestMavenAndNpxRefuseASymlinkedCacheDir(t *testing.T) {
	home, _ := isolateToolCaches(t)
	shared := t.TempDir()
	keep := filepath.Join(shared, "settings.xml")
	writeSized(t, keep, 5)
	writeSized(t, filepath.Join(shared, "other/data"), 5)
	if err := os.MkdirAll(filepath.Join(home, ".m2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".npm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(home, ".m2/repository")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(home, ".npm/_npx")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"maven", "npx"} {
		req := httptest.NewRequest("DELETE", "/cleanup/tool-caches/"+name, nil)
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		handleDeleteToolCache(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s: status %d, want 409 (%s)", name, rec.Code, rec.Body)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("link target was emptied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(shared, "other/data")); err != nil {
		t.Fatalf("link target was emptied: %v", err)
	}
	rec := httptest.NewRecorder()
	handleToolCacheUsage(rec, httptest.NewRequest("GET", "/cleanup/tool-caches", nil))
	if strings.Contains(rec.Body.String(), `"maven"`) || strings.Contains(rec.Body.String(), `"npx"`) {
		t.Fatalf("symlinked caches listed: %s", rec.Body)
	}
}
