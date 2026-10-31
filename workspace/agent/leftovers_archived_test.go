package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// archivedFixture is a fixed clock plus helpers to lay out archived sessions with marked
// ~/.af-work folders.
type archivedFixture struct {
	t    *testing.T
	home string
	now  time.Time
}

func newArchivedFixture(t *testing.T) *archivedFixture {
	home, _ := isolateLeftovers(t)
	f := &archivedFixture{t: t, home: home, now: time.Now().Add(30 * 24 * time.Hour)}
	pruneNow = func() time.Time { return f.now }
	return f
}

func (f *archivedFixture) work() string { return filepath.Join(f.home, ".af-work") }

// session writes an archived meta; at "" leaves archivedAt unset.
func (f *archivedFixture) session(name string, archived bool, at string, locked bool) {
	f.t.Helper()
	m := map[string]any{"name": name, "dir": filepath.Join(f.home, "elsewhere", "wc-"+name), "kind": "claude",
		"archived": archived, "locked": locked}
	if at != "" {
		m["archivedAt"] = at
	}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(f.home, "sessions", name+".json"), b, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *archivedFixture) folder(name string, marked bool) {
	f.t.Helper()
	writeSized(f.t, filepath.Join(f.work(), name, "x"), 10)
	if marked {
		if err := paths.MarkSessionWorkDir(filepath.Join(f.work(), name)); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *archivedFixture) ago(d time.Duration) string {
	return f.now.Add(-d).UTC().Format(time.RFC3339)
}

const day = 24 * time.Hour

func TestArchivedAfWorkRemovedAfterSevenDaysOnly(t *testing.T) {
	f := newArchivedFixture(t)
	f.session("sold001", true, f.ago(7*day+time.Minute), false)
	f.session("sedge01", true, f.ago(7*day), false) // exactly the boundary: due
	f.session("sjust01", true, f.ago(7*day-time.Minute), false)
	f.session("slive01", false, "", false)
	for _, n := range []string{"sold001", "sedge01", "sjust01", "slive01"} {
		f.folder(n, true)
	}

	r := pruneKindBoot(t, "af-work")

	assertEntries(t, f.work(), "sjust01", "slive01")
	if r.Count != 2 {
		t.Fatalf("removed = %+v", r)
	}
}

// A session archived before archivedAt existed is stamped, not removed, on the first pass.
func TestArchivedAfWorkWithoutDateIsStampedNotRemoved(t *testing.T) {
	f := newArchivedFixture(t)
	f.session("sprev01", true, "", false)
	f.folder("sprev01", true)

	pruneKindBoot(t, "af-work")

	assertEntries(t, f.work(), "sprev01")
	m, _ := session.ReadMeta("sprev01")
	if m.ArchivedAt != f.now.UTC().Format(time.RFC3339) || !m.Archived {
		t.Fatalf("meta after first pass: %+v", m)
	}
	// A week later the stamp is due.
	f.now = f.now.Add(7 * day)
	pruneKindBoot(t, "af-work")
	assertEntries(t, f.work())
}

// The stamp is written onto the meta as it is, never a snapshot: other fields survive.
func TestArchivedAfWorkStampKeepsOtherFields(t *testing.T) {
	f := newArchivedFixture(t)
	f.session("slock01", true, "", true)
	stampArchivedSessions()
	m, _ := session.ReadMeta("slock01")
	if !m.Locked || m.ArchivedAt == "" {
		t.Fatalf("meta: %+v", m)
	}
}

func TestArchivedAfWorkKeepsLockedUnmarkedAndWorkingCopyNamed(t *testing.T) {
	f := newArchivedFixture(t)
	old := f.ago(30 * day)
	f.session("slock01", true, old, true)
	f.session("sunmk01", true, old, false)
	f.session("swc0001", true, old, false)
	f.folder("slock01", true)
	f.folder("sunmk01", false)
	// A Managed session keys its folder by the working copy: a live repo of that name stays.
	if err := os.MkdirAll(filepath.Join(f.home, "repos", "swc0001"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.folder("swc0001", true)

	pruneKindBoot(t, "af-work")
	assertEntries(t, f.work(), "slock01", "sunmk01", "swc0001")

	// The button takes the unmarked one, as it does any unmarked orphan; never the locked one.
	pruneKind(t, "af-work")
	assertEntries(t, f.work(), "slock01", "swc0001")
}

// A restore that lands after the listing and before the removal keeps the folder. The
// candidates func restores right after listing, so only prune's own stillDue guard can save it.
func TestArchivedAfWorkRestoreRacingThePruneKeepsTheFolder(t *testing.T) {
	f := newArchivedFixture(t)
	f.session("srace01", true, f.ago(10*day), false)
	f.folder("srace01", true)
	k, _ := findLeftoverKind("af-work")
	k.prepare = nil
	listed := k.candidates
	k.candidates = func(h string, r []string, p map[string]string) []leftover {
		out := listed(h, r, p)
		if len(out) != 1 || out[0].stillDue == nil {
			t.Fatalf("candidates = %+v", out)
		}
		if _, ok := sessionx.RestoreSession("srace01"); !ok {
			t.Fatal("restore failed")
		}
		return out
	}
	pins, _ := workspacePins()
	r, err := k.prune(homeDir(), pins, true)
	if err != nil || r.Count != 0 {
		t.Fatalf("prune = %+v, %v", r, err)
	}
	assertEntries(t, f.work(), "srace01")
}

// An expired session whose name is another session's working directory name stays.
func TestArchivedAfWorkKeepsNameOfAnotherSessionsWorkingDir(t *testing.T) {
	f := newArchivedFixture(t)
	f.session("swc0002", true, f.ago(30*day), false)
	f.session("slive02", false, "", false)
	b, _ := json.Marshal(map[string]any{"name": "slive02", "dir": filepath.Join(f.home, "x", "swc0002"), "kind": "claude"})
	if err := os.WriteFile(filepath.Join(f.home, "sessions", "slive02.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	f.folder("swc0002", true)
	pruneKindBoot(t, "af-work")
	assertEntries(t, f.work(), "swc0002")
}

// A stamp far in the future (a clock that was once wrong) is replaced, so the folder is not
// kept until the clock catches up.
func TestArchivedAfWorkFutureStampIsRestamped(t *testing.T) {
	f := newArchivedFixture(t)
	f.session("sfut001", true, f.now.Add(400*day).UTC().Format(time.RFC3339), false)
	f.folder("sfut001", true)
	pruneKindBoot(t, "af-work")
	assertEntries(t, f.work(), "sfut001")
	m, _ := session.ReadMeta("sfut001")
	if m.ArchivedAt != f.now.UTC().Format(time.RFC3339) {
		t.Fatalf("archivedAt = %q", m.ArchivedAt)
	}
}

// A meta that does not parse means no name can be trusted: nothing is removed or stamped.
func TestArchivedAfWorkBrokenMetaDeletesNothing(t *testing.T) {
	f := newArchivedFixture(t)
	f.session("sold001", true, f.ago(30*day), false)
	f.folder("sold001", true)
	if err := os.WriteFile(filepath.Join(f.home, "sessions", "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := afWorkKnownNames(); ok {
		t.Fatal("afWorkKnownNames ok with an unparseable meta")
	}
	pruneKindBoot(t, "af-work")
	assertEntries(t, f.work(), "sold001")
}

// An expired session's symlinked folder is never followed.
func TestArchivedAfWorkSymlinkedFolderIsLeft(t *testing.T) {
	f := newArchivedFixture(t)
	f.session("slink01", true, f.ago(30*day), false)
	target := filepath.Join(f.home, "precious")
	writeSized(t, filepath.Join(target, "keep"), 1)
	if err := os.MkdirAll(f.work(), 0o755); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, target, filepath.Join(f.work(), "slink01"))
	pruneKind(t, "af-work")
	assertEntries(t, target, "keep")
}

func TestPruneLeftoversDailyRunsEveryTickAndStops(t *testing.T) {
	f := newArchivedFixture(t)
	f.session("sold001", true, f.ago(30*day), false)
	f.folder("sold001", true)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { pruneLeftoversDaily(10*time.Millisecond, stop); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(f.work(), "sold001")); os.IsNotExist(err) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(stop)
	<-done
	assertEntries(t, f.work())
}
