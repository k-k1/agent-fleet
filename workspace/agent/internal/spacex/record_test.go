package spacex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readMarkerFile(t *testing.T, dir string) marker {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, MarkerFile))
	if err != nil {
		t.Fatalf("marker: %v", err)
	}
	var m marker
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
	if b, _ := os.ReadFile(marker); string(b) != theirs {
		t.Fatalf("marker = %q, want theirs kept", b)
	}
	afterRepair = func() {
		_ = os.Remove(marker)
		_ = os.Mkdir(marker, 0o755)
	}
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
