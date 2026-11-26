package spacex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func readMarkerFile(t *testing.T, dir string) markerOut {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, MarkerFile))
	if err != nil {
		t.Fatalf("marker: %v", err)
	}
	var m markerOut
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("marker %q: %v", b, err)
	}
	return m
}

// A marker the Agent writes says what it is for, so an agent that opens it before tidying knows
// not to delete it.
func TestCreatedMarkerCarriesTheNote(t *testing.T) {
	withHome(t)
	sp, _ := Create("Notes")
	if m := readMarkerFile(t, sp.Dir); m.Note != markerNote || !strings.Contains(m.Note, "Do not delete") {
		t.Fatalf("note = %q", m.Note)
	}
}

// The heart of #5231: a removed marker is written back with the recorded name and creation time,
// and the space stays in the list, on its session rows and in Lookup.
func TestRemovedMarkerIsRestored(t *testing.T) {
	for name, read := range map[string]func(Space) bool{
		"FolderOf": func(sp Space) bool { return FolderOf(sp.Dir) == sp.Folder },
		"Lookup": func(sp Space) bool {
			got, ok := Lookup(sp.Folder)
			return ok && got.Name == "Quarterly report"
		},
		"List": func(sp Space) bool {
			list, err := List()
			return err == nil && len(list) == 1 && list[0].Folder == sp.Folder && list[0].Name == "Quarterly report"
		},
	} {
		t.Run(name, func(t *testing.T) {
			withHome(t)
			sp, err := Create("Quarterly report")
			if err != nil {
				t.Fatal(err)
			}
			before := readMarkerFile(t, sp.Dir)
			if err := os.Remove(filepath.Join(sp.Dir, MarkerFile)); err != nil {
				t.Fatal(err)
			}
			if !read(sp) {
				t.Fatalf("%s dropped the space whose marker was removed", name)
			}
			after := readMarkerFile(t, sp.Dir)
			if after.Name != before.Name || after.CreatedAt != before.CreatedAt || after.Note != markerNote {
				t.Fatalf("restored marker = %+v, was %+v", after, before)
			}
			fi, err := os.Lstat(filepath.Join(sp.Dir, MarkerFile))
			if err != nil || !fi.Mode().IsRegular() {
				t.Fatalf("restored marker is no regular file: %v %v", fi, err)
			}
			ents, _ := os.ReadDir(sp.Dir)
			if len(ents) != 1 {
				t.Fatalf("the repair left files behind: %v", ents)
			}
		})
	}
}

// A space made before the record existed is recorded by the first list, and from then on its
// marker is restored too.
func TestListRecordsSpacesMadeBeforeTheRecord(t *testing.T) {
	withHome(t)
	dir := filepath.Join(Root(), "old-abcdef")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, MarkerFile), []byte(`{"name":"Old","createdAt":"2026-01-01T00:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := recorded("old-abcdef"); ok {
		t.Fatal("recorded before any list")
	}
	if list, _ := List(); len(list) != 1 {
		t.Fatalf("List() = %+v", list)
	}
	if e, ok := recorded("old-abcdef"); !ok || e.Name != "Old" || e.CreatedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("record = %+v, %v", e, ok)
	}
	_ = os.Remove(filepath.Join(dir, MarkerFile))
	if got, ok := Lookup("old-abcdef"); !ok || got.Name != "Old" {
		t.Fatalf("Lookup = %+v, %v", got, ok)
	}
}

// What is no space stays none: a folder made by hand is never recorded, and a non-file in the
// marker's place (a directory, a FIFO, a link) is neither replaced nor counted.
func TestRepairWritesOnlyAnAbsentMarkerOfARecordedSpace(t *testing.T) {
	withHome(t)
	byHand := filepath.Join(Root(), "by-hand")
	if err := os.MkdirAll(byHand, 0o755); err != nil {
		t.Fatal(err)
	}
	if FolderOf(byHand) != "" {
		t.Fatal("an unrecorded folder became a space")
	}
	if _, err := os.Lstat(filepath.Join(byHand, MarkerFile)); err == nil {
		t.Fatal("a marker was written into a folder made by hand")
	}
	sp, _ := Create("Draft")
	marker := filepath.Join(sp.Dir, MarkerFile)
	_ = os.Remove(marker)
	if err := os.Mkdir(marker, 0o755); err != nil {
		t.Fatal(err)
	}
	if FolderOf(sp.Dir) != "" {
		t.Fatal("a directory in the marker's place made a space")
	}
	if fi, err := os.Lstat(marker); err != nil || !fi.IsDir() {
		t.Fatalf("the directory in the marker's place was replaced: %v %v", fi, err)
	}
}

// A marker that appears between the repair's check and its write (another Agent request, the
// member) is kept, not overwritten.
func TestRepairDoesNotReplaceAMarkerWrittenMeanwhile(t *testing.T) {
	withHome(t)
	sp, _ := Create("Draft")
	marker := filepath.Join(sp.Dir, MarkerFile)
	_ = os.Remove(marker)
	theirs := `{"name":"theirs"}`
	afterRepair = func() { _ = os.WriteFile(marker, []byte(theirs), 0o644) }
	t.Cleanup(func() { afterRepair = nil })
	if !repairMarker(sp.Folder) {
		t.Fatal("repair reported no marker though one was written")
	}
	if ents, _ := os.ReadDir(sp.Dir); len(ents) != 1 {
		t.Fatalf("the repair left its temporary file: %v", ents)
	}
	if b, _ := os.ReadFile(marker); string(b) != theirs {
		t.Fatalf("marker = %q, want theirs kept", b)
	}
	_ = os.Remove(marker)
	afterRepair = func() { _ = os.Mkdir(marker, 0o755) }
	if repairMarker(sp.Folder) {
		t.Fatal("a directory in the marker's place counted as a marker")
	}
}

// Deleting a space forgets it: a folder made later by hand under the same name gets no marker.
// Restoring it records it again.
func TestMoveOutForgetsAndMoveInRemembers(t *testing.T) {
	home := withHome(t)
	sp, _ := Create("Draft")
	dest := filepath.Join(home, "trash-entry")
	if err := MoveOut(sp.Folder, dest); err != nil {
		t.Fatal(err)
	}
	if _, ok := recorded(sp.Folder); ok {
		t.Fatal("a moved-out space is still recorded")
	}
	if err := os.Mkdir(sp.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if FolderOf(sp.Dir) != "" {
		t.Fatal("a folder made by hand after the delete became the space again")
	}
	if err := os.Remove(sp.Dir); err != nil {
		t.Fatal(err)
	}
	if err := MoveIn(dest, sp.Folder); err != nil {
		t.Fatal(err)
	}
	if e, ok := recorded(sp.Folder); !ok || e.Name != "Draft" {
		t.Fatalf("record after MoveIn = %+v, %v", e, ok)
	}
	_ = os.Remove(filepath.Join(sp.Dir, MarkerFile))
	if FolderOf(sp.Dir) != sp.Folder {
		t.Fatal("the restored space lost its marker for good")
	}
}

// A rename updates the record, so a marker removed afterwards comes back with the new name; and
// a space whose marker is missing can still be renamed.
func TestRenameKeepsTheRecordCurrent(t *testing.T) {
	withHome(t)
	sp, _ := Create("Draft")
	_ = os.Remove(filepath.Join(sp.Dir, MarkerFile))
	if _, err := Rename(sp.Folder, "Final"); err != nil {
		t.Fatalf("Rename of a space whose marker was removed = %v", err)
	}
	_ = os.Remove(filepath.Join(sp.Dir, MarkerFile))
	if got, ok := Lookup(sp.Folder); !ok || got.Name != "Final" {
		t.Fatalf("Lookup = %+v, %v; want the renamed name restored", got, ok)
	}
}

func TestProtected(t *testing.T) {
	home := withHome(t)
	sp, _ := Create("Draft")
	gone, _ := Create("Gone marker")
	_ = os.Remove(filepath.Join(gone.Dir, MarkerFile))
	byHand := filepath.Join(Root(), "by-hand")
	if err := os.MkdirAll(byHand, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(Root(), filepath.Join(home, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sp.Dir, filepath.Join(home, "link-to-space")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(home, filepath.Join(home, "homealias")); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]bool{
		Root():                                   true,
		Root() + "/":                             true,
		sp.Dir:                                   true,
		filepath.Join(sp.Dir, MarkerFile):        true,
		gone.Dir:                                 true, // recorded: still protected while the marker is gone
		filepath.Join(gone.Dir, MarkerFile):      true,
		filepath.Join(home, "alias", sp.Folder):  true,
		filepath.Join(home, "homealias/spaces"):  true,
		filepath.Join(sp.Dir, "notes.md"):        false,
		filepath.Join(sp.Dir, "attachments"):     false,
		filepath.Join(sp.Dir, "sub", MarkerFile): false,
		byHand:                                   false,
		filepath.Join(byHand, MarkerFile):        false,
		filepath.Join(home, "link-to-space"):     false, // removing the link leaves the space alone
		filepath.Join(home, "alias"):             false,
		home:                                     false,
		"spaces/" + sp.Folder:                    false, // relative: not this function's to judge
		"":                                       false,
	} {
		if got := Protected(p); got != want {
			t.Errorf("Protected(%q) = %v, want %v", p, got, want)
		}
	}
}

// A space moved away by hand and replaced by another entry of the same name is not that space:
// the new folder gets no marker and is not protected, and a file there can be deleted as before.
func TestReplacedFolderIsNotTheRecordedSpace(t *testing.T) {
	home := withHome(t)
	sp, _ := Create("Draft")
	_ = os.Remove(filepath.Join(sp.Dir, MarkerFile))
	if err := os.Rename(sp.Dir, filepath.Join(home, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sp.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if FolderOf(sp.Dir) != "" {
		t.Fatal("an ordinary folder in the recorded space's place became the space")
	}
	if _, err := os.Lstat(filepath.Join(sp.Dir, MarkerFile)); err == nil {
		t.Fatal("the old space's marker was written into another folder")
	}
	if Protected(sp.Dir) {
		t.Fatal("an ordinary folder in the recorded space's place is protected")
	}
	if err := os.Remove(sp.Dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sp.Dir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if Protected(sp.Dir) || Protected(filepath.Join(sp.Dir, MarkerFile)) {
		t.Fatal("a regular file in the recorded space's place is protected")
	}
}

// A folder moved away while a repair is writing gets nothing written at its new place.
func TestRepairWritesNothingIntoAFolderMovedMeanwhile(t *testing.T) {
	home := withHome(t)
	sp, _ := Create("Draft")
	_ = os.Remove(filepath.Join(sp.Dir, MarkerFile))
	moved := filepath.Join(home, "moved")
	afterRepair = func() { _ = os.Rename(sp.Dir, moved) }
	t.Cleanup(func() { afterRepair = nil })
	if repairMarker(sp.Folder) {
		t.Fatal("repair reported a marker in a folder that moved away")
	}
	ents, _ := os.ReadDir(moved)
	if len(ents) != 0 {
		t.Fatalf("the repair wrote into the moved folder: %v", ents)
	}
	afterRepair = func() { _ = os.Rename(Root(), filepath.Join(home, "spaces-moved")) }
	if err := os.Rename(moved, sp.Dir); err != nil {
		t.Fatal(err)
	}
	if repairMarker(sp.Folder) {
		t.Fatal("repair reported a marker after ~/spaces moved away")
	}
	if _, err := os.Lstat(filepath.Join(home, "spaces-moved", sp.Folder, MarkerFile)); err == nil {
		t.Fatal("the repair wrote into the moved ~/spaces")
	}
}

// A list read before a delete does not record the deleted space again (syncRecord re-checks the
// folder under lifeMu).
func TestSyncAfterADeleteDoesNotRecordTheSpaceAgain(t *testing.T) {
	home := withHome(t)
	sp, _ := Create("Draft")
	rows, _ := List()
	if err := MoveOut(sp.Folder, filepath.Join(home, "trash-entry")); err != nil {
		t.Fatal(err)
	}
	syncRecord(rows)
	if _, ok := recorded(sp.Folder); ok {
		t.Fatal("a stale list recorded the deleted space again")
	}
}

// List corrects an entry the marker contradicts (a rename whose record write failed), and keeps
// the recorded name when the marker does not parse.
func TestListResyncsTheRecordFromTheMarker(t *testing.T) {
	withHome(t)
	sp, _ := Create("Draft")
	if _, err := Rename(sp.Folder, "Final"); err != nil {
		t.Fatal(err)
	}
	_ = updateRecord(func(m map[string]recordEntry) bool {
		e := m[sp.Folder]
		e.Name = "Draft"
		m[sp.Folder] = e
		return true
	})
	_, _ = List()
	if e, _ := recorded(sp.Folder); e.Name != "Final" {
		t.Fatalf("record name = %q after List, want the marker's", e.Name)
	}
	if err := os.WriteFile(filepath.Join(sp.Dir, MarkerFile), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = List()
	if e, _ := recorded(sp.Folder); e.Name != "Final" {
		t.Fatalf("an unparsable marker overwrote the recorded name: %q", e.Name)
	}
}

// A record that is there but cannot be read is not replaced by an update that knows one space.
func TestUnreadableRecordIsNotOverwritten(t *testing.T) {
	withHome(t)
	first, _ := Create("First")
	if _, ok := recorded(first.Folder); !ok { // primes the cache: a chmod keeps size and mtime
		t.Fatal("not recorded")
	}
	if err := os.Chmod(recordPath(), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(recordPath(), 0o600) })
	if f, err := os.Open(recordPath()); err == nil {
		f.Close()
		t.Skip("running as a user that reads mode 000 files")
	}
	before, err := os.Lstat(recordPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create("Second"); err != nil {
		t.Fatal(err)
	}
	if after, err := os.Lstat(recordPath()); err != nil || !os.SameFile(before, after) {
		t.Fatal("an update replaced a record it could not read (answered from the cache)")
	}
	_ = os.Chmod(recordPath(), 0o600)
	if _, ok := recorded(first.Folder); !ok {
		t.Fatal("an update over an unreadable record lost the existing entries")
	}
}

// A FIFO in the record's place blocks neither the list nor a create.
func TestFIFORecordDoesNotBlock(t *testing.T) {
	withHome(t)
	sp, _ := Create("Draft")
	_ = os.Remove(recordPath())
	if err := syscall.Mkfifo(recordPath(), 0o600); err != nil {
		t.Skip("no mkfifo here:", err)
	}
	recordMu.Lock()
	recordCache.ok = false
	recordMu.Unlock()
	done := make(chan struct{})
	go func() {
		_, _ = List()
		_, _ = Create("Other")
		_ = FolderOf(sp.Dir)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a FIFO record blocked the list or a create")
	}
	if _, ok := recorded(sp.Folder); !ok {
		t.Fatal("the list did not record the space again after replacing the FIFO")
	}
}

// A restored space with an old marker that names nothing ({}) is still recorded, so its marker
// is restored even before the next list.
func TestMoveInRecordsAMarkerWithoutAName(t *testing.T) {
	home := withHome(t)
	sp, _ := Create("Draft")
	if err := os.WriteFile(filepath.Join(sp.Dir, MarkerFile), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(home, "trash-entry")
	if err := MoveOut(sp.Folder, dest); err != nil {
		t.Fatal(err)
	}
	if err := MoveIn(dest, sp.Folder); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(sp.Dir, MarkerFile))
	if FolderOf(sp.Dir) != sp.Folder {
		t.Fatal("a restored space with a nameless marker was not recorded")
	}
}

// A note of another type in an existing marker does not cost the name: the reader does not type
// the note.
func TestMarkerWithAnOddNoteKeepsItsName(t *testing.T) {
	withHome(t)
	sp, _ := Create("Draft")
	if err := os.WriteFile(filepath.Join(sp.Dir, MarkerFile), []byte(`{"name":"Mine","note":[1,2]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := Lookup(sp.Folder); !ok || got.Name != "Mine" {
		t.Fatalf("Lookup = %+v, %v", got, ok)
	}
	if _, err := Rename(sp.Folder, "Renamed"); err != nil {
		t.Fatal(err)
	}
	if got, ok := Lookup(sp.Folder); !ok || got.Name != "Renamed" {
		t.Fatalf("Lookup after Rename = %+v, %v", got, ok)
	}
}

// ~/spaces moved while the Agent runs (after its resolved path was cached) and replaced by a link
// to the new place: a space is still protected through either spelling.
func TestProtectedAfterSpacesMovedBehindALink(t *testing.T) {
	home := withHome(t)
	sp, _ := Create("Draft")
	_ = realRoot() // warm its cache: FolderOf on the plain spelling never asks it
	store := filepath.Join(home, "store")
	if err := os.Rename(Root(), store); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(store, Root()); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{sp.Dir, filepath.Join(store, sp.Folder), filepath.Join(sp.Dir, MarkerFile)} {
		if !Protected(p) {
			t.Errorf("Protected(%q) = false after ~/spaces moved behind a link", p)
		}
	}
}

// A rename that runs while a list is syncing the record is not undone by the list's older read.
func TestRenameDuringSyncKeepsTheNewName(t *testing.T) {
	withHome(t)
	sp, _ := Create("Draft")
	renamed := make(chan error, 1)
	afterSyncRead = func() {
		afterSyncRead = nil
		if renameMu.TryLock() {
			renameMu.Unlock()
			t.Error("syncRecord does not hold renameMu between its read and its write")
		}
		go func() { _, err := Rename(sp.Folder, "Final"); renamed <- err }()
		time.Sleep(100 * time.Millisecond) // long enough for an unserialised rename to finish
	}
	t.Cleanup(func() { afterSyncRead = nil })
	if _, err := List(); err != nil {
		t.Fatal(err)
	}
	if err := <-renamed; err != nil {
		t.Fatal(err)
	}
	if e, _ := recorded(sp.Folder); e.Name != "Final" {
		t.Fatalf("record name = %q, want the rename's", e.Name)
	}
}

// A directory born after the recorded one (an inode the file system reused for a folder made
// later) is not the recorded space.
func TestAnotherBirthTimeIsAnotherDirectory(t *testing.T) {
	withHome(t)
	sp, _ := Create("Draft")
	e, _ := recorded(sp.Folder)
	if e.Ino == 0 || e.Born == 0 {
		t.Skip("this file system keeps no birth time, so no identity is recorded")
	}
	_ = updateRecord(func(m map[string]recordEntry) bool {
		x := m[sp.Folder]
		x.Born--
		m[sp.Folder] = x
		return true
	})
	_ = os.Remove(filepath.Join(sp.Dir, MarkerFile))
	if FolderOf(sp.Dir) != "" || Protected(sp.Dir) {
		t.Fatal("a directory with another birth time was taken for the recorded space")
	}
}

// A recorded creation time too long for the full marker (copied from an old one) still gives a
// marker every reader reads, with its name.
func TestRepairStaysWithinTheMarkerBound(t *testing.T) {
	withHome(t)
	dir := filepath.Join(Root(), "old-abcdef")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"name":"Old","createdAt":"` + strings.Repeat("x", markerMaxBytes-40) + `"}`
	if err := os.WriteFile(filepath.Join(dir, MarkerFile), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if list, _ := List(); len(list) != 1 || list[0].Name != "Old" {
		t.Fatalf("List() = %+v", list)
	}
	_ = os.Remove(filepath.Join(dir, MarkerFile))
	if got, ok := Lookup("old-abcdef"); !ok || got.Name != "Old" {
		t.Fatalf("Lookup = %+v, %v", got, ok)
	}
	if fi, err := os.Stat(filepath.Join(dir, MarkerFile)); err != nil || fi.Size() > markerMaxBytes {
		t.Fatalf("restored marker: %v %v", fi, err)
	}
}

// A folder swapped for another by a shell while a list syncs the record does not tie the space's
// name to the other folder: the identity recorded is the one the marker was read from.
func TestSyncDoesNotRecordAFolderSwappedMeanwhile(t *testing.T) {
	home := withHome(t)
	sp, _ := Create("Draft")
	if e, _ := recorded(sp.Folder); e.Born == 0 {
		t.Skip("this file system keeps no birth time, so no identity is recorded")
	}
	afterSyncRead = func() {
		afterSyncRead = nil
		_ = os.Rename(sp.Dir, filepath.Join(home, "moved"))
		_ = os.Mkdir(sp.Dir, 0o755)
	}
	t.Cleanup(func() { afterSyncRead = nil })
	if _, err := List(); err != nil {
		t.Fatal(err)
	}
	if FolderOf(sp.Dir) != "" || Protected(sp.Dir) {
		t.Fatal("the folder swapped in during the sync was taken for the space")
	}
	if _, err := os.Lstat(filepath.Join(sp.Dir, MarkerFile)); err == nil {
		t.Fatal("the space's marker was written into the swapped-in folder")
	}
}

// An entry without a birth time (a file system that keeps none) matches no directory, so an inode
// reused for a folder made later can never be taken for the space.
func TestEntryWithoutABirthTimeMatchesNothing(t *testing.T) {
	withHome(t)
	sp, _ := Create("Draft")
	_ = updateRecord(func(m map[string]recordEntry) bool {
		x := m[sp.Folder]
		x.Born = 0
		m[sp.Folder] = x
		return true
	})
	_ = os.Remove(filepath.Join(sp.Dir, MarkerFile))
	if FolderOf(sp.Dir) != "" || Protected(sp.Dir) {
		t.Fatal("an entry with no birth time matched by inode alone")
	}
}

// An update whose identity could not be had (statx failed) keeps the recorded identity whole.
func TestUpdateWithoutAnIdentityKeepsTheRecordedOne(t *testing.T) {
	withHome(t)
	remember("draft-abcdef", recordEntry{Name: "Draft", Ino: 123, Born: 456})
	remember("draft-abcdef", entryWith(dirID{}, "Other", ""))
	if e, _ := recorded("draft-abcdef"); e != (recordEntry{Name: "Other", Ino: 123, Born: 456}) {
		t.Fatalf("entry = %+v", e)
	}
}

// A rename records the identity of the folder it published into, not of what the path names
// afterwards.
func TestRenameRecordsThePublishedFolder(t *testing.T) {
	home := withHome(t)
	sp, _ := Create("Draft")
	// Unrecorded, so an identity the rename failed to take cannot be made up for by Create's.
	forget(sp.Folder)
	moved := filepath.Join(home, "moved")
	afterPublish = func() {
		if err := os.Rename(sp.Dir, moved); err != nil {
			t.Error(err)
		}
		if err := os.Mkdir(sp.Dir, 0o755); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterPublish = nil })
	if _, err := Rename(sp.Folder, "Final"); err != nil {
		t.Fatal(err)
	}
	want, ok := idOf(moved)
	if !ok {
		t.Skip("this file system keeps no birth time, so no identity is recorded")
	}
	if e, _ := recorded(sp.Folder); e.Ino != want.ino || e.Born != want.born {
		t.Fatalf("recorded identity %d/%d, want the published folder's %d/%d", e.Ino, e.Born, want.ino, want.born)
	}
}

// An old marker whose name Lookup drops (too long), and whose JSON grows past the bound when
// encoded again, is still restored: the record keeps the name Lookup showed (none).
func TestRepairOfANameLookupDrops(t *testing.T) {
	withHome(t)
	dir := filepath.Join(Root(), "old-abcdef")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"name":"` + strings.Repeat("<", 1000) + `"}`
	if err := os.WriteFile(filepath.Join(dir, MarkerFile), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if list, _ := List(); len(list) != 1 || list[0].Name != "" {
		t.Fatalf("List() = %+v", list)
	}
	e, ok := recorded("old-abcdef")
	if !ok {
		t.Skip("this file system keeps no birth time, so the list records no identity")
	}
	if e.Name != "" {
		t.Fatalf("recorded name = %q, want none (Lookup drops it)", e.Name)
	}
	_ = os.Remove(filepath.Join(dir, MarkerFile))
	if FolderOf(dir) != "old-abcdef" {
		t.Fatal("the space whose name Lookup drops was not restored")
	}
}
