package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// isolateLeftovers points home, /proc, the session store, versions.json and the hostname
// at test values, so nothing on the machine running the test is judged or removed.
func isolateLeftovers(t *testing.T) (home, proc string) {
	t.Helper()
	home, proc = isolateCLIVersions(t)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	if err := os.MkdirAll(filepath.Join(home, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	pins := filepath.Join(t.TempDir(), "versions.json")
	if err := os.WriteFile(pins, []byte(`{"kiro":"2.24.1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPins, oldHost := buildPinsPath, hostnameFn
	buildPinsPath = pins
	hostnameFn = func() (string, error) { return "thisbox", nil }
	t.Cleanup(func() { buildPinsPath, hostnameFn = oldPins, oldHost })
	return home, proc
}

func pruneKind(t *testing.T, name string) leftoverRemoved {
	t.Helper()
	k, ok := findLeftoverKind(name)
	if !ok {
		t.Fatalf("no kind %s", name)
	}
	pins, _ := workspacePins()
	r, err := k.prune(homeDir(), pins)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func assertEntries(t *testing.T, dir string, want ...string) {
	t.Helper()
	got := entries(t, dir)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", dir, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: got %v, want %v", dir, got, want)
		}
	}
}

// chromiumProfile lays down a throwaway profile; lock "" means no SingletonLock.
func chromiumProfile(t *testing.T, dir, lock string) {
	t.Helper()
	writeSized(t, filepath.Join(dir, "Default", "History"), 512)
	if lock != "" {
		mustSymlink(t, lock, filepath.Join(dir, "SingletonLock"))
	}
}

func TestLeftoversChromiumFollowsTheSingletonLock(t *testing.T) {
	home, proc := isolateLeftovers(t)
	cfg := filepath.Join(home, ".config", "chromium-headless")
	cache := filepath.Join(home, ".cache", "chromium-headless")
	fakeProc(t, proc, "4242", "chromium")
	chromiumProfile(t, filepath.Join(cfg, "scoped_dirOther"), "e2f0c73711f4-4242") // recreated container
	chromiumProfile(t, filepath.Join(cfg, "scoped_dirLive"), "thisbox-4242")
	chromiumProfile(t, filepath.Join(cfg, "scoped_dirDead"), "thisbox-999")
	chromiumProfile(t, filepath.Join(cfg, "scoped_dirNoLock"), "")
	chromiumProfile(t, filepath.Join(cfg, "scoped_dirOdd"), "not-a-pid")
	chromiumProfile(t, filepath.Join(cfg, "Default"), "")
	writeSized(t, filepath.Join(cfg, "Local State"), 10)
	// The cache half has no lock and follows its twin; one without a twin is orphaned.
	writeSized(t, filepath.Join(cache, "scoped_dirOther", "Default", "Cache", "x"), 256)
	writeSized(t, filepath.Join(cache, "scoped_dirLive", "Default", "Cache", "x"), 256)
	writeSized(t, filepath.Join(cache, "scoped_dirAlone", "Default", "Cache", "x"), 256)
	// Another chromium's user-data dir, not named scoped_dir: never a candidate.
	chromiumProfile(t, filepath.Join(home, ".config", "chromium", "Profile 1"), "")

	r := pruneKind(t, "chromium")

	assertEntries(t, cfg, "Default", "Local State", "scoped_dirLive", "scoped_dirOdd")
	assertEntries(t, cache, "scoped_dirLive")
	assertEntries(t, filepath.Join(home, ".config", "chromium"), "Profile 1")
	if r.Count != 5 || r.Bytes != 3*512+2*256 {
		t.Fatalf("removed = %+v", r)
	}
}

func writeMeta(t *testing.T, home, name, dir string) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"name": name, "dir": dir, "kind": "claude"})
	if err := os.WriteFile(filepath.Join(home, "sessions", name+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLeftoversAfWorkKeepsSessionsTrashAndWorkingCopies(t *testing.T) {
	home, _ := isolateLeftovers(t)
	writeMeta(t, home, "slive01", filepath.Join(home, "elsewhere", "proj@wip-slive01"))
	man := cleanupManifest{ID: "20260927-000000-strash1", Sessions: []cleanupArchivedSession{{Name: "strash1"}}}
	b, _ := json.Marshal(man)
	writeSized(t, filepath.Join(sessionx.CleanupArchiveDir(), "x"), 0)
	if err := os.WriteFile(filepath.Join(sessionx.CleanupArchiveDir(), man.ID+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "repos", "app@feature-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(home, ".af-work")
	for _, n := range []string{"slive01", "proj@wip-slive01", "strash1", "app@feature-1", "sgone01", "old-repo@wip-sgone02"} {
		writeSized(t, filepath.Join(work, n, "probe.txt"), 100)
	}
	writeSized(t, filepath.Join(work, "loose-file"), 5)

	r := pruneKind(t, "af-work")

	assertEntries(t, work, "slive01", "proj@wip-slive01", "strash1", "app@feature-1", "loose-file")
	if r.Count != 2 || r.Bytes != 200 {
		t.Fatalf("removed = %+v", r)
	}
}

// Without the session store every directory would look orphaned.
func TestLeftoversAfWorkNeedsTheSessionStore(t *testing.T) {
	home, _ := isolateLeftovers(t)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "missing"))
	writeSized(t, filepath.Join(home, ".af-work", "sgone01", "x"), 1)

	pruneKind(t, "af-work")

	assertEntries(t, filepath.Join(home, ".af-work"), "sgone01")
}

func TestLeftoversNodeKeepsTheHighestPatchPerMajor(t *testing.T) {
	home, _ := isolateLeftovers(t)
	root := nvmNodeRoot()
	for _, v := range []string{"v22.9.0", "v22.23.1", "v22.23.2", "v22.23.10", "v20.1.0", "v24.0.0", "v24.1.0"} {
		writeSized(t, filepath.Join(root, v, "bin", "node"), 64)
	}
	writeSized(t, filepath.Join(root, "system", "x"), 1)
	writeSized(t, filepath.Join(root, ".install-24.2.0-abc", "bin", "node"), 1)
	// An alias pinned to an exact version keeps it; one naming a major changes nothing.
	writeSized(t, filepath.Join(home, ".nvm", "alias", "default"), 0)
	if err := os.WriteFile(filepath.Join(home, ".nvm", "alias", "default"), []byte("22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".nvm", "alias", "old"), []byte("v24.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// nvm's LTS index names exact versions too, but nobody chose them.
	writeSized(t, filepath.Join(home, ".nvm", "alias", "lts", "jod"), 0)
	if err := os.WriteFile(filepath.Join(home, ".nvm", "alias", "lts", "jod"), []byte("v22.23.2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// What the entrypoint put on the Agent's PATH stays even when it is not the highest.
	t.Setenv("PATH", filepath.Join(root, "v22.23.1", "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := pruneKind(t, "node")

	assertEntries(t, root, ".install-24.2.0-abc", "system", "v20.1.0", "v22.23.1", "v22.23.10", "v24.0.0", "v24.1.0")
	if r.Count != 2 {
		t.Fatalf("removed = %+v", r)
	}
}

func TestLeftoversNodeKeepsAVersionInUse(t *testing.T) {
	home, proc := isolateLeftovers(t)
	_ = home
	root := nvmNodeRoot()
	writeSized(t, filepath.Join(root, "v22.23.1", "bin", "node"), 64)
	writeSized(t, filepath.Join(root, "v22.23.2", "bin", "node"), 64)
	fakeProcFiles(t, proc, "77", filepath.Join(root, "v22.23.1", "bin", "node"), nil, nil)

	pruneKind(t, "node")

	assertEntries(t, root, "v22.23.1", "v22.23.2")
}

func kas(t *testing.T, root, name, lockPID string) {
	t.Helper()
	writeSized(t, filepath.Join(root, name, "kiro-cli-chat"), 128)
	if lockPID != "" {
		if err := os.WriteFile(filepath.Join(root, name+".lock"), []byte(`{"pid":`+lockPID+`,"acquired_at_ms":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLeftoversKiroKeepsInstalledPinnedHighestAndLocked(t *testing.T) {
	home, proc := isolateLeftovers(t)
	root := kiroKasRoots(home)[0]
	writeSized(t, filepath.Join(home, ".local", "bin", ".kiro.version"), 0)
	if err := os.WriteFile(filepath.Join(home, ".local", "bin", ".kiro.version"), []byte("2.16.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeProc(t, proc, "555", "kiro-cli-chat")
	kas(t, root, "2.13.0-aa", "")
	kas(t, root, "2.14.1-bb", "999") // lock of a dead pid
	kas(t, root, "2.15.0-cc", "555") // lock of a live pid
	kas(t, root, "2.16.0-dd", "1")   // installed
	kas(t, root, "2.24.1-ee", "")    // pinned
	kas(t, root, "2.30.0-ff", "")    // highest present
	writeSized(t, filepath.Join(root, "notes"), 1)

	r := pruneKind(t, "kiro")

	assertEntries(t, root, "2.15.0-cc", "2.15.0-cc.lock", "2.16.0-dd", "2.16.0-dd.lock", "2.24.1-ee", "2.30.0-ff", "notes")
	if r.Count != 2 {
		t.Fatalf("removed = %+v", r)
	}
}

// Without install-kiro's marker which version is current cannot be told.
func TestLeftoversKiroNeedsTheInstalledMarker(t *testing.T) {
	home, _ := isolateLeftovers(t)
	root := kiroKasRoots(home)[0]
	kas(t, root, "2.13.0-aa", "")
	kas(t, root, "2.16.0-dd", "")

	pruneKind(t, "kiro")

	assertEntries(t, root, "2.13.0-aa", "2.16.0-dd")
}

func TestLeftoversKeepFreshAndInUseDirectories(t *testing.T) {
	home, proc := isolateLeftovers(t)
	work := filepath.Join(home, ".af-work")
	writeSized(t, filepath.Join(work, "sbusy01", "x"), 1)
	writeSized(t, filepath.Join(work, "sgone01", "x"), 1)
	fakeProcFiles(t, proc, "88", "/usr/bin/bash", nil, nil)
	mustSymlink(t, filepath.Join(work, "sbusy01"), filepath.Join(proc, "88", "cwd"))

	pruneKind(t, "af-work")
	assertEntries(t, work, "sbusy01")

	// Back on the real clock, a directory made moments ago is left for the next pass.
	pruneNow = time.Now
	writeSized(t, filepath.Join(work, "snew001", "x"), 1)
	pruneKind(t, "af-work")
	assertEntries(t, work, "sbusy01", "snew001")
}

// A symlink in a root is not ours to follow, whatever its name.
func TestLeftoversLeaveSymlinks(t *testing.T) {
	home, _ := isolateLeftovers(t)
	target := filepath.Join(home, "precious")
	writeSized(t, filepath.Join(target, "keep"), 1)
	mustSymlink(t, target, filepath.Join(home, ".af-work", "sgone01"))

	pruneKind(t, "af-work")

	assertEntries(t, filepath.Join(home, ".af-work"), "sgone01")
	assertEntries(t, target, "keep")
}

func TestLeftoversSurveyMatchesTheDelete(t *testing.T) {
	home, _ := isolateLeftovers(t)
	chromiumProfile(t, filepath.Join(home, ".config", "chromium-headless", "scoped_dirA"), "")
	writeSized(t, filepath.Join(home, ".af-work", "sgone01", "x"), 300)

	rec := httptest.NewRecorder()
	handleLeftoverUsage(rec, httptest.NewRequest("GET", "/cleanup/leftovers", nil))
	var u leftoverUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}
	got := map[string]leftoverRow{}
	for _, r := range u.Kinds {
		got[r.Kind] = r
	}
	if len(u.Kinds) != len(leftoverKinds) || got["af-work"].Count != 1 || got["af-work"].Bytes != 300 ||
		got["chromium"].Count != 1 || got["node"].Count != 0 || got["af-work"].Path != "~/.af-work" {
		t.Fatalf("survey = %+v", u)
	}

	req := httptest.NewRequest("DELETE", "/cleanup/leftovers/af-work", nil)
	req.SetPathValue("kind", "af-work")
	rec = httptest.NewRecorder()
	handleDeleteLeftovers(rec, req)
	var res leftoverRemoved
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusOK || res.Count != 1 || res.Bytes != 300 {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	assertEntries(t, filepath.Join(home, ".af-work"))
	// Only the kind asked for goes.
	assertEntries(t, filepath.Join(home, ".config", "chromium-headless"), "scoped_dirA")

	req = httptest.NewRequest("DELETE", "/cleanup/leftovers/nope", nil)
	req.SetPathValue("kind", "nope")
	rec = httptest.NewRecorder()
	handleDeleteLeftovers(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown kind = %d", rec.Code)
	}
}

// Outside a Workspace image home is not ours: the boot pass does nothing and the survey
// says so.
func TestLeftoversNeedVersionsJSON(t *testing.T) {
	home, _ := isolateLeftovers(t)
	buildPinsPath = filepath.Join(t.TempDir(), "missing.json")
	writeSized(t, filepath.Join(home, ".af-work", "sgone01", "x"), 1)

	pruneLeftoversAtBoot()
	assertEntries(t, filepath.Join(home, ".af-work"), "sgone01")

	rec := httptest.NewRecorder()
	handleLeftoverUsage(rec, httptest.NewRequest("GET", "/cleanup/leftovers", nil))
	var u leftoverUsage
	_ = json.Unmarshal(rec.Body.Bytes(), &u)
	if !u.Unsupported || len(u.Kinds) != 0 {
		t.Fatalf("survey = %s", rec.Body.String())
	}
}

// ListMetas skips a meta it cannot parse, and WriteMeta is not atomic: a meta caught
// mid-write must not make its session's directory look orphaned, nor must a broken manifest
// in the trash.
func TestLeftoversAfWorkFailsClosedOnAnUnreadableRecord(t *testing.T) {
	home, _ := isolateLeftovers(t)
	writeSized(t, filepath.Join(home, ".af-work", "shalf01", "x"), 1)
	if err := os.WriteFile(filepath.Join(home, "sessions", "shalf01.json"), []byte(`{"name":"shal`), 0o600); err != nil {
		t.Fatal(err)
	}
	pruneKind(t, "af-work")
	assertEntries(t, filepath.Join(home, ".af-work"), "shalf01")

	writeMeta(t, home, "shalf01", "")
	writeSized(t, filepath.Join(sessionx.CleanupArchiveDir(), "20260927-000000-sx.json"), 0)
	pruneKind(t, "af-work")
	assertEntries(t, filepath.Join(home, ".af-work"), "shalf01")
}

// nodeBinFor only picks a version with bin/: a higher directory without it must not count
// as the one sessions run.
func TestLeftoversNodeIgnoresAVersionThatCannotRun(t *testing.T) {
	isolateLeftovers(t)
	root := nvmNodeRoot()
	writeSized(t, filepath.Join(root, "v20.1.0", "bin", "node"), 64)
	writeSized(t, filepath.Join(root, "v20.2.0", "include", "node.h"), 8)

	pruneKind(t, "node")

	assertEntries(t, root, "v20.1.0", "v20.2.0")
}

func TestDeleteLeftoversReportsAPartialFailure(t *testing.T) {
	home, _ := isolateLeftovers(t)
	work := filepath.Join(home, ".af-work")
	writeSized(t, filepath.Join(work, "sgone01", "x"), 10)
	writeSized(t, filepath.Join(work, "sgone02", "x"), 10)
	old := removeLeftover
	t.Cleanup(func() { removeLeftover = old })
	removeLeftover = func(p string) error {
		if filepath.Base(p) == "sgone02" {
			return os.ErrPermission
		}
		return removeTree(p)
	}
	del := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("DELETE", "/cleanup/leftovers/af-work", nil)
		req.SetPathValue("kind", "af-work")
		rec := httptest.NewRecorder()
		handleDeleteLeftovers(rec, req)
		return rec
	}

	// One went, one did not: 200 with the count and what failed.
	rec := del()
	var res leftoverRemoved
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusOK || res.Count != 1 || res.Failed == "" {
		t.Fatalf("partial = %d %s", rec.Code, rec.Body.String())
	}
	// Nothing went: an error.
	if rec := del(); rec.Code != http.StatusInternalServerError {
		t.Fatalf("all failed = %d %s", rec.Code, rec.Body.String())
	}
}
