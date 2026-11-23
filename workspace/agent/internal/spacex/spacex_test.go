package spacex

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestCreateMakesAMarkedFolderUnderRoot(t *testing.T) {
	home := withHome(t)
	sp, err := Create("Q3 report: sales & costs")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := filepath.Dir(sp.Dir), filepath.Join(home, "spaces"); got != want {
		t.Fatalf("space dir %q is not directly under %q", sp.Dir, want)
	}
	if !strings.HasPrefix(sp.Folder, "q3-report-sales-costs-") {
		t.Fatalf("folder %q does not start with the name's slug", sp.Folder)
	}
	if sp.Name != "Q3 report: sales & costs" {
		t.Fatalf("name = %q", sp.Name)
	}
	got, ok := Lookup(sp.Folder)
	if !ok || got.Name != sp.Name || got.Dir != sp.Dir {
		t.Fatalf("Lookup(%q) = %+v, %v", sp.Folder, got, ok)
	}
	if FolderOf(sp.Dir) != sp.Folder {
		t.Fatalf("FolderOf(%q) = %q", sp.Dir, FolderOf(sp.Dir))
	}
}

func TestCreateWithoutASCIIFallsBackToSpace(t *testing.T) {
	withHome(t)
	sp, err := Create("議事録のまとめ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sp.Folder, "space-") || !ValidFolder(sp.Folder) {
		t.Fatalf("folder = %q", sp.Folder)
	}
	if sp.Name != "議事録のまとめ" {
		t.Fatalf("name = %q", sp.Name)
	}
}

// Two creates never share a folder, even when the random half repeats: the folder is claimed
// with Mkdir, and a taken one is skipped.
func TestCreateSkipsATakenFolder(t *testing.T) {
	withHome(t)
	seq := []string{"aaaaaa", "aaaaaa", "bbbbbb"}
	prev := randSuffix
	randSuffix = func() string { s := seq[0]; seq = seq[1:]; return s }
	t.Cleanup(func() { randSuffix = prev })
	a, err := Create("x")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Create("x")
	if err != nil {
		t.Fatal(err)
	}
	if a.Folder != "x-aaaaaa" || b.Folder != "x-bbbbbb" {
		t.Fatalf("folders = %q, %q", a.Folder, b.Folder)
	}
	if got, _ := Lookup(a.Folder); got.Name != "x" {
		t.Fatalf("the first space's marker was overwritten: %+v", got)
	}
}

func TestListSkipsUnmarkedFoldersAndRepositories(t *testing.T) {
	home := withHome(t)
	if got, err := List(); err != nil || len(got) != 0 {
		t.Fatalf("List() with no ~/spaces = %v, %v; want empty, nil", got, err)
	}
	sp, err := Create("kept")
	if err != nil {
		t.Fatal(err)
	}
	// A folder made by hand under ~/spaces carries no marker and is not a space.
	if err := os.MkdirAll(filepath.Join(home, "spaces", "by-hand"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Folder != sp.Folder {
		t.Fatalf("List() = %+v", got)
	}
	// A repository of the same folder name is not a space: the root decides, not the name.
	if f := FolderOf(filepath.Join(home, "repos", sp.Folder)); f != "" {
		t.Fatalf("FolderOf(repo) = %q, want empty", f)
	}
	if f := FolderOf(filepath.Join(sp.Dir, "sub")); f != "" {
		t.Fatalf("FolderOf(subfolder) = %q, want empty", f)
	}
}

func TestLookupRefusesFoldersOutsideRoot(t *testing.T) {
	withHome(t)
	for _, f := range []string{"", "..", "../repos", "a/b", "-x", "X"} {
		if _, ok := Lookup(f); ok {
			t.Errorf("Lookup(%q) found a space", f)
		}
	}
}

func TestCleanName(t *testing.T) {
	if got, ok := CleanName("  notes  "); !ok || got != "notes" {
		t.Fatalf("CleanName trims: %q, %v", got, ok)
	}
	if _, ok := CleanName("a\nb"); ok {
		t.Fatal("a control character was accepted")
	}
	if _, ok := CleanName(strings.Repeat("あ", NameMaxRunes+1)); ok {
		t.Fatal("an overlong name was accepted")
	}
	if _, ok := CleanName(strings.Repeat("あ", NameMaxRunes)); !ok {
		t.Fatal("a name at the limit was refused")
	}
}

func TestRemoveIfUnusedKeepsASpaceWithFiles(t *testing.T) {
	withHome(t)
	empty, _ := Create("empty")
	used, _ := Create("used")
	if err := os.WriteFile(filepath.Join(used.Dir, "notes.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	RemoveIfUnused(empty.Dir)
	RemoveIfUnused(used.Dir)
	if _, err := os.Stat(empty.Dir); !os.IsNotExist(err) {
		t.Fatalf("the unused space is still there: %v", err)
	}
	if _, ok := Lookup(used.Folder); !ok {
		t.Fatal("a space with a file in it was removed")
	}
}

func TestHandlers(t *testing.T) {
	home := withHome(t)
	rec := httptest.NewRecorder()
	HandleCreate(rec, httptest.NewRequest(http.MethodPost, "/spaces", strings.NewReader(`{"name":"Draft"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var created createResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.Space.Name != "Draft" {
		t.Fatalf("create body %s: %v", rec.Body, err)
	}

	rec = httptest.NewRecorder()
	HandleCreate(rec, httptest.NewRequest(http.MethodPost, "/spaces", strings.NewReader(`{"name":"a\u0007b"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad name: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	HandleList(rec, httptest.NewRequest(http.MethodGet, "/spaces", nil))
	var list listResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Root != filepath.Join(home, "spaces") || len(list.Spaces) != 1 || list.Spaces[0].Folder != created.Space.Folder {
		t.Fatalf("list = %+v", list)
	}
}
