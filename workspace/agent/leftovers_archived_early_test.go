package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// deleteLeftoverKind calls the DELETE handler for kind with the given query.
func deleteLeftoverKind(t *testing.T, kind, query string) (*httptest.ResponseRecorder, leftoverRemoved) {
	t.Helper()
	req := httptest.NewRequest("DELETE", "/cleanup/leftovers/"+kind+query, nil)
	req.SetPathValue("kind", kind)
	rec := httptest.NewRecorder()
	handleDeleteLeftovers(rec, req)
	var res leftoverRemoved
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	return rec, res
}

// surveyLeftovers returns the GET /cleanup/leftovers rows by kind.
func surveyLeftovers(t *testing.T) map[string]leftoverRow {
	t.Helper()
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
	return got
}

// The early removal takes archived sessions inside the week, and only those: the af-work row
// keeps counting orphans and expired folders, and no folder is in both rows.
func TestArchivedEarlyTakesOnlyShelvedSessionsInsideTheWeek(t *testing.T) {
	f := newArchivedFixture(t)
	f.session("snew001", true, f.ago(day), false)
	f.session("snodate", true, "", false)
	f.session("sold001", true, f.ago(30*day), false)
	f.session("slive01", false, "", false)
	for _, n := range []string{"snew001", "snodate", "sold001", "slive01"} {
		f.folder(n, true)
	}
	f.folder("sgone01", true) // no session at all
	f.folder("snewunm", false)
	f.session("snewunm", true, f.ago(day), false)

	got := surveyLeftovers(t)
	if got["af-work-archived"].Count != 3 || got["af-work-archived"].Path != "~/.af-work" || !got["af-work-archived"].Confirm ||
		got["af-work"].Confirm {
		t.Fatalf("af-work-archived = %+v", got["af-work-archived"])
	}
	if got["af-work"].Count != 2 { // sold001 (expired) and sgone01 (orphan)
		t.Fatalf("af-work = %+v", got["af-work"])
	}

	rec, res := deleteLeftoverKind(t, "af-work-archived", "?confirm=1")
	if rec.Code != http.StatusOK || res.Count != 3 {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	// The unmarked one goes too: this is a confirmed press, as for the af-work button.
	assertEntries(t, f.work(), "sgone01", "slive01", "sold001")
}

// Without confirm=1 nothing goes: an older Console offers the row with a plain delete button.
func TestArchivedEarlyNeedsConfirm(t *testing.T) {
	f := newArchivedFixture(t)
	f.session("snew001", true, f.ago(day), false)
	f.folder("snew001", true)
	for _, q := range []string{"", "?confirm=0", "?confirm=true"} {
		rec, _ := deleteLeftoverKind(t, "af-work-archived", q)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%q: %d %s", q, rec.Code, rec.Body.String())
		}
	}
	assertEntries(t, f.work(), "snew001")
	// The kinds that need no confirmation still do not.
	if rec, _ := deleteLeftoverKind(t, "af-work", ""); rec.Code != http.StatusOK {
		t.Fatalf("af-work without confirm = %d", rec.Code)
	}
}

// The unattended pass never takes a folder the week has not reached, whatever the kind list
// holds: pruneLeftoversAtBoot skips manualOnly kinds, and prune refuses them with boot=true.
func TestArchivedEarlyNeverRunsUnattended(t *testing.T) {
	f := newArchivedFixture(t)
	f.session("snew001", true, f.ago(day), false)
	f.folder("snew001", true)

	pruneLeftoversAtBoot()
	assertEntries(t, f.work(), "snew001")
	if r := pruneKindBoot(t, "af-work-archived"); r.Count != 0 {
		t.Fatalf("boot prune = %+v", r)
	}
	assertEntries(t, f.work(), "snew001")
}

func TestArchivedEarlyKeepsLockedWorkingCopyTrashFreshAndSymlinks(t *testing.T) {
	f := newArchivedFixture(t)
	recent := f.ago(day)
	f.session("slock01", true, recent, true)
	f.folder("slock01", true)
	// A live repo of that name: a Managed session's folder.
	f.session("swc0001", true, recent, false)
	f.folder("swc0001", true)
	if err := os.MkdirAll(filepath.Join(f.home, "repos", "swc0001"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Another live session's working directory is named like it.
	f.session("swd0001", true, recent, false)
	f.folder("swd0001", true)
	b, _ := json.Marshal(map[string]any{"name": "slive02", "dir": filepath.Join(f.home, "x", "swd0001"), "kind": "claude"})
	if err := os.WriteFile(filepath.Join(f.home, "sessions", "slive02.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	// Changed within freshVersionAge.
	f.session("sfresh1", true, recent, false)
	f.folder("sfresh1", true)
	if err := os.Chtimes(filepath.Join(f.work(), "sfresh1"), f.now, f.now); err != nil {
		t.Fatal(err)
	}
	// A symlinked folder is never followed.
	f.session("slink01", true, recent, false)
	target := filepath.Join(f.home, "precious")
	writeSized(t, filepath.Join(target, "keep"), 1)
	mustSymlink(t, target, filepath.Join(f.work(), "slink01"))

	pruneKind(t, "af-work-archived")

	assertEntries(t, f.work(), "sfresh1", "slink01", "slock01", "swc0001", "swd0001")
	assertEntries(t, target, "keep")
}

// A restore or a lock that lands after the listing and before the removal keeps the folder.
func TestArchivedEarlyRestoreOrLockRacingThePruneKeepsTheFolder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after func(t *testing.T, name string)
	}{
		{"restore", func(t *testing.T, n string) {
			if _, ok := sessionx.RestoreSession(n); !ok {
				t.Fatal("restore failed")
			}
		}},
		{"lock", func(t *testing.T, n string) {
			sessionx.UpdateSessionMeta(n, func(m *session.Meta) bool { m.Locked = true; return true })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newArchivedFixture(t)
			f.session("srace01", true, f.ago(day), false)
			f.folder("srace01", true)
			k, _ := findLeftoverKind("af-work-archived")
			listed := k.candidates
			k.candidates = func(h string, r []string, p map[string]string) []leftover {
				out := listed(h, r, p)
				if len(out) != 1 || out[0].stillDue == nil {
					t.Fatalf("candidates = %+v", out)
				}
				tc.after(t, "srace01")
				return out
			}
			pins, _ := workspacePins()
			r, err := k.prune(homeDir(), pins, false)
			if err != nil || r.Count != 0 {
				t.Fatalf("prune = %+v, %v", r, err)
			}
			assertEntries(t, f.work(), "srace01")
		})
	}
}

// A meta that does not parse means no name can be trusted.
func TestArchivedEarlyBrokenMetaDeletesNothing(t *testing.T) {
	f := newArchivedFixture(t)
	f.session("snew001", true, f.ago(day), false)
	f.folder("snew001", true)
	if err := os.WriteFile(filepath.Join(f.home, "sessions", "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	pruneKind(t, "af-work-archived")
	assertEntries(t, f.work(), "snew001")
}

// A restore that lands once the removal has begun cannot lose what the relaunched session
// writes: the folder is renamed away under the meta lock first, so the session gets a fresh
// one at the old path and only the detached copy is removed.
func TestArchivedEarlyRestoreDuringTheRemovalKeepsNewFiles(t *testing.T) {
	for _, kind := range []string{"af-work-archived", "af-work"} {
		t.Run(kind, func(t *testing.T) {
			f := newArchivedFixture(t)
			at := f.ago(day)
			if kind == "af-work" {
				at = f.ago(30 * day)
			}
			f.session("srace01", true, at, false)
			f.folder("srace01", true)
			old := removeLeftover
			t.Cleanup(func() { removeLeftover = old })
			removeLeftover = func(p string) error {
				if p == filepath.Join(f.work(), "srace01") {
					t.Fatalf("removed in place: %s", p)
				}
				if _, ok := sessionx.RestoreSession("srace01"); !ok {
					t.Fatal("restore failed")
				}
				writeSized(t, filepath.Join(f.work(), "srace01", "new"), 1)
				return removeTree(p)
			}
			if r := pruneKind(t, kind); r.Count != 1 {
				t.Fatalf("prune = %+v", r)
			}
			assertEntries(t, f.work(), "srace01")
			assertEntries(t, filepath.Join(f.work(), "srace01"), "new")
		})
	}
}

// A detached copy a failed removal left behind goes with the next af-work press.
func TestArchivedEarlyDetachedLeftoverIsAnOrphanNextTime(t *testing.T) {
	f := newArchivedFixture(t)
	f.session("snew001", true, f.ago(day), false)
	f.folder("snew001", true)
	old := removeLeftover
	t.Cleanup(func() { removeLeftover = old })
	removeLeftover = func(string) error { return os.ErrPermission }
	k, _ := findLeftoverKind("af-work-archived")
	pins, _ := workspacePins()
	if r, err := k.prune(homeDir(), pins, false); err == nil || r.Count != 0 {
		t.Fatalf("prune = %+v, %v", r, err)
	}
	if got := entries(t, f.work()); len(got) != 1 || got[0] == "snew001" {
		t.Fatalf("after a failed removal: %v", got)
	}
	removeLeftover = old
	pruneKind(t, "af-work")
	assertEntries(t, f.work())
}
