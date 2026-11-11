package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

type ageFixture struct {
	t    *testing.T
	home string
	proc string
}

func newAgeFixture(t *testing.T) *ageFixture {
	home, proc := isolateLeftovers(t)
	return &ageFixture{t: t, home: home, proc: proc}
}

func (f *ageFixture) work() string { return filepath.Join(f.home, ".af-work") }

// folder lays out ~/.af-work/<name>/sub/x and dates every entry age ago.
func (f *ageFixture) folder(name string, marked bool, age time.Duration) string {
	f.t.Helper()
	dir := filepath.Join(f.work(), name)
	writeSized(f.t, filepath.Join(dir, "sub", "x"), 100)
	if marked {
		if err := paths.MarkSessionWorkDir(dir); err != nil {
			f.t.Fatal(err)
		}
	}
	f.age(name, age)
	return dir
}

func (f *ageFixture) age(name string, age time.Duration) {
	f.t.Helper()
	at := time.Now().Add(-age)
	_ = filepath.WalkDir(filepath.Join(f.work(), name), func(p string, _ os.DirEntry, _ error) error {
		ts := unix.NsecToTimespec(at.UnixNano())
		_ = unix.UtimesNanoAt(unix.AT_FDCWD, p, []unix.Timespec{ts, ts}, unix.AT_SYMLINK_NOFOLLOW)
		return nil
	})
}

func names(rows []afWorkOldRow) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]bool{}
	for _, x := range a {
		m[x] = true
	}
	for _, x := range b {
		if !m[x] {
			return false
		}
	}
	return true
}

func (f *ageFixture) list(days int) afWorkOldList {
	pins, _ := workspacePins()
	return afWorkOldFolders(f.home, pins, days)
}

func (f *ageFixture) remove(days int, n ...string) afWorkOldRemoved {
	pins, _ := workspacePins()
	return removeOldAfWork(f.home, pins, days, n)
}

func TestAfWorkAgeOffersOnlyOldUnmarkedUnusedFolders(t *testing.T) {
	f := newAgeFixture(t)
	f.folder("old-own", false, 40*day)
	f.folder("young-own", false, 3*day)
	f.folder("old-marked", true, 40*day) // the boot pass owns marked ones
	f.folder("live-session", false, 40*day)
	f.folder("wc-name", false, 40*day)
	writeMeta(t, f.home, "live-session", filepath.Join(f.home, "elsewhere", "wc-name"))

	got := names(f.list(30).Folders)
	if !eq(got, []string{"old-own"}) {
		t.Fatalf("offered %v, want only old-own (a live session's and a working copy's folder, marked and young ones stay)", got)
	}
	if !eq(names(f.list(2).Folders), []string{"old-own", "young-own"}) {
		t.Fatalf("2-day cutoff: %v", names(f.list(2).Folders))
	}
}

func TestAfWorkAgeUsesNewestFileNotTheFolderTime(t *testing.T) {
	f := newAgeFixture(t)
	dir := f.folder("busy", false, 40*day)
	// A deep, recent write leaves the folder's own mtime old.
	fresh := filepath.Join(dir, "sub", "x")
	now := time.Now().Add(-1 * day)
	if err := os.Chtimes(fresh, now, now); err != nil {
		t.Fatal(err)
	}
	if got := names(f.list(30).Folders); len(got) != 0 {
		t.Fatalf("a folder with a day-old file inside was offered at 30 days: %v", got)
	}
}

func TestAfWorkAgeRemovalRejudgesWhatTheCallerNames(t *testing.T) {
	f := newAgeFixture(t)
	f.folder("old-own", false, 40*day)
	f.folder("young-own", false, 3*day)
	f.folder("old-marked", true, 40*day)
	f.folder("live-session", false, 40*day)
	writeMeta(t, f.home, "live-session", filepath.Join(f.home, "elsewhere", "x"))

	res := f.remove(30, "old-own", "young-own", "old-marked", "live-session", "../old-own", "nope", "")
	if !eq(names(res.Removed), []string{"old-own"}) {
		t.Fatalf("removed %v", names(res.Removed))
	}
	if len(res.Skipped) != 6 {
		t.Fatalf("skipped %+v", res.Skipped)
	}
	left := entries(t, f.work())
	if !eq(left, []string{"young-own", "old-marked", "live-session"}) {
		t.Fatalf("left %v (a detached .removing- dir must not survive either)", left)
	}
}

func TestAfWorkAgeRefusesAFolderThatChangedSinceTheList(t *testing.T) {
	f := newAgeFixture(t)
	f.folder("a", false, 40*day)
	if got := names(f.list(30).Folders); !eq(got, []string{"a"}) {
		t.Fatal(got)
	}
	// Written after the survey, before the click.
	writeSized(t, filepath.Join(f.work(), "a", "new"), 5)
	res := f.remove(30, "a")
	if len(res.Removed) != 0 || len(res.Skipped) != 1 || res.Skipped[0].Reason != "not_eligible" {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(filepath.Join(f.work(), "a", "new")); err != nil {
		t.Fatal("the fresh file is gone")
	}
}

func TestAfWorkAgeNeverFollowsSymlinks(t *testing.T) {
	f := newAgeFixture(t)
	outside := t.TempDir()
	writeSized(t, filepath.Join(outside, "precious"), 10)
	// A folder that is itself a link out of ~/.af-work, and a link inside an old folder.
	if err := os.MkdirAll(f.work(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.work(), "link")); err != nil {
		t.Fatal(err)
	}
	f.folder("holder", false, 40*day)
	if err := os.Symlink(outside, filepath.Join(f.work(), "holder", "escape")); err != nil {
		t.Fatal(err)
	}
	f.age("holder", 40*day)

	if got := names(f.list(30).Folders); !eq(got, []string{"holder"}) {
		t.Fatalf("offered %v: a symlink in ~/.af-work must not be offered", got)
	}
	res := f.remove(30, "link", "holder")
	if !eq(names(res.Removed), []string{"holder"}) {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(filepath.Join(outside, "precious")); err != nil {
		t.Fatalf("removal followed a link: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(f.work(), "link")); err != nil {
		t.Fatalf("the link itself was touched: %v", err)
	}
}

func TestAfWorkAgeRefusesASymlinkedRoot(t *testing.T) {
	f := newAgeFixture(t)
	real := filepath.Join(t.TempDir(), "elsewhere")
	writeSized(t, filepath.Join(real, "old", "x"), 10)
	_ = os.Chtimes(filepath.Join(real, "old", "x"), time.Now().Add(-40*day), time.Now().Add(-40*day))
	_ = os.Chtimes(filepath.Join(real, "old"), time.Now().Add(-40*day), time.Now().Add(-40*day))
	if err := os.Symlink(real, f.work()); err != nil {
		t.Fatal(err)
	}
	if got := f.list(30).Folders; len(got) != 0 {
		t.Fatalf("offered %v through a symlinked ~/.af-work", names(got))
	}
}

func TestAfWorkAgeKeepsFoldersAProcessHolds(t *testing.T) {
	f := newAgeFixture(t)
	f.folder("held", false, 40*day)
	fakeProcFiles(t, f.proc, "88", "/usr/bin/bash", nil, nil)
	mustSymlink(t, filepath.Join(f.work(), "held"), filepath.Join(f.proc, "88", "cwd"))
	if got := names(f.list(30).Folders); len(got) != 0 {
		t.Fatalf("a folder a process runs in was offered: %v", got)
	}
}

func TestAfWorkAgeHTTP(t *testing.T) {
	f := newAgeFixture(t)
	f.folder("old-own", false, 40*day)
	f.folder("young", false, 1*day)
	get := func(q string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handleAfWorkOldList(w, httptest.NewRequest("GET", "/cleanup/af-work"+q, nil))
		return w
	}
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handleAfWorkOldRemove(w, httptest.NewRequest("POST", "/cleanup/af-work", bytes.NewBufferString(body)))
		return w
	}
	for _, q := range []string{"", "?older_than_days=0", "?older_than_days=-1", "?older_than_days=x", "?older_than_days=99999"} {
		if w := get(q); w.Code != http.StatusBadRequest {
			t.Fatalf("GET %q: %d", q, w.Code)
		}
	}
	for _, b := range []string{`{`, `{"older_than_days":0,"names":["a"]}`, `{"older_than_days":30,"names":[]}`} {
		if w := post(b); w.Code != http.StatusBadRequest {
			t.Fatalf("POST %s: %d", b, w.Code)
		}
	}
	var list afWorkOldList
	if err := json.Unmarshal(get("?older_than_days=30").Body.Bytes(), &list); err != nil || !eq(names(list.Folders), []string{"old-own"}) {
		t.Fatalf("%v %+v", err, list)
	}
	w := post(`{"older_than_days":30,"names":["old-own"]}`)
	var res afWorkOldRemoved
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != 200 || !eq(names(res.Removed), []string{"old-own"}) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestAfWorkAgePutsBackAFolderWrittenAfterTheDetach(t *testing.T) {
	f := newAgeFixture(t)
	f.folder("a", false, 40*day)
	afWorkAfterDetach = func(target string) { writeSized(t, filepath.Join(target, "sub", "late"), 5) }
	t.Cleanup(func() { afWorkAfterDetach = nil })
	res := f.remove(30, "a")
	if len(res.Removed) != 0 || len(res.Skipped) != 1 || res.Skipped[0].Reason != "changed" {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(filepath.Join(f.work(), "a", "sub", "late")); err != nil {
		t.Fatalf("the folder was not put back: %v", err)
	}
	if got := entries(t, f.work()); !eq(got, []string{"a"}) {
		t.Fatalf("left %v", got)
	}
}
