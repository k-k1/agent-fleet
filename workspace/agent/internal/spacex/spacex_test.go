package spacex

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

// FolderOf answers the same space whichever way its directory is spelled, and only for a
// marked folder.
func TestFolderOfSpellings(t *testing.T) {
	real := t.TempDir()
	alias := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", alias)
	sp, err := Create("x")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(real, "spaces", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{
		sp.Dir,
		sp.Dir + "/",
		filepath.Join(real, "spaces", sp.Folder), // through the symlink's target
		alias + "/spaces/./" + sp.Folder,
		alias + "/spaces/sub/../" + sp.Folder, // sub exists below: a real directory
	} {
		if got := FolderOf(dir); got != sp.Folder {
			t.Errorf("FolderOf(%q) = %q, want %q", dir, got, sp.Folder)
		}
	}
	for _, dir := range []string{"", "spaces/" + sp.Folder, filepath.Join(sp.Dir, "sub")} {
		if got := FolderOf(dir); got != "" {
			t.Errorf("FolderOf(%q) = %q, want empty", dir, got)
		}
	}
}

// A ".." after a symlink is resolved, not dropped: ~/spaces/link/../<folder> runs in the
// link target's parent, which is no space even when ~/spaces/<folder> is one.
func TestFolderOfResolvesDotDotAfterASymlink(t *testing.T) {
	home := withHome(t)
	sp, err := Create("notes")
	if err != nil {
		t.Fatal(err)
	}
	repoChild := filepath.Join(home, "repos", "child")
	if err := os.MkdirAll(repoChild, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "repos", sp.Folder), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repoChild, filepath.Join(Root(), "link")); err != nil {
		t.Fatal(err)
	}
	if got := FolderOf(filepath.Join(Root(), "link") + "/../" + sp.Folder); got != "" {
		t.Fatalf("FolderOf(link/../folder) = %q, want empty: it runs in ~/repos/%s", got, sp.Folder)
	}
}

// A marker the Agent cannot read still marks the space, for the session row, the list and a
// create naming the folder alike; only its name is lost.
func TestUnreadableMarkerStillMarksTheSpace(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any file")
	}
	withHome(t)
	sp, _ := Create("secret")
	if err := os.Chmod(filepath.Join(sp.Dir, MarkerFile), 0); err != nil {
		t.Fatal(err)
	}
	if FolderOf(sp.Dir) != sp.Folder {
		t.Fatal("FolderOf dropped the space")
	}
	got, ok := Lookup(sp.Folder)
	if !ok || got.Name != "" {
		t.Fatalf("Lookup = %+v, %v; want the space without a name", got, ok)
	}
	if list, _ := List(); len(list) != 1 || list[0].Folder != sp.Folder {
		t.Fatalf("List() = %+v", list)
	}
}

// A folder under ~/spaces with no marker is no space: a session that already ran there before
// spaces existed keeps its place in the Console.
func TestFolderOfNeedsTheMarker(t *testing.T) {
	home := withHome(t)
	dir := filepath.Join(home, "spaces", "by-hand")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := FolderOf(dir); got != "" {
		t.Fatalf("FolderOf(unmarked) = %q, want empty", got)
	}
	// A marker that is a directory, not a file, does not count either.
	if err := os.Mkdir(filepath.Join(dir, MarkerFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := FolderOf(dir); got != "" {
		t.Fatalf("FolderOf(directory marker) = %q, want empty", got)
	}
}

// A symlink in ~/spaces is no space for Lookup, FolderOf or List alike, so a link to a marked
// folder outside the root cannot be started in as an existing space.
func TestSymlinkedFolderIsNoSpace(t *testing.T) {
	home := withHome(t)
	outside := filepath.Join(home, "elsewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, MarkerFile), []byte(`{"name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(Root(), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(Root(), "link-abcdef")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, ok := Lookup("link-abcdef"); ok {
		t.Fatal("Lookup followed a symlink out of the root")
	}
	if got := FolderOf(link); got != "" {
		t.Fatalf("FolderOf(symlink) = %q", got)
	}
	if got, _ := List(); len(got) != 0 {
		t.Fatalf("List() = %+v", got)
	}
}

// A marker replaced by a FIFO or a symlink is skipped without blocking the list; the space
// beside it is still listed.
func TestOddMarkersDoNotHangTheList(t *testing.T) {
	withHome(t)
	good, _ := Create("good")
	fifo, _ := Create("fifo")
	link, _ := Create("link")
	os.Remove(filepath.Join(fifo.Dir, MarkerFile))
	if err := syscall.Mkfifo(filepath.Join(fifo.Dir, MarkerFile), 0o644); err != nil {
		t.Skip("no mkfifo here:", err)
	}
	os.Remove(filepath.Join(link.Dir, MarkerFile))
	if err := os.Symlink(filepath.Join(good.Dir, MarkerFile), filepath.Join(link.Dir, MarkerFile)); err != nil {
		t.Fatal(err)
	}
	done := make(chan []Space, 1)
	go func() { got, _ := List(); done <- got }()
	select {
	case got := <-done:
		if len(got) != 1 || got[0].Folder != good.Folder {
			t.Fatalf("List() = %+v, want only %s", got, good.Folder)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("List() blocked on a FIFO marker")
	}
	if FolderOf(fifo.Dir) != "" || FolderOf(link.Dir) != "" {
		t.Fatal("a FIFO or symlink marker made a space")
	}
}

// An oversized marker still marks the space; its name is dropped rather than read whole.
func TestOversizedMarkerLosesOnlyTheName(t *testing.T) {
	withHome(t)
	sp, _ := Create("big")
	big := `{"name":"` + strings.Repeat("a", markerMaxBytes) + `"}`
	if err := os.WriteFile(filepath.Join(sp.Dir, MarkerFile), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := Lookup(sp.Folder)
	if !ok || got.Name != "" {
		t.Fatalf("Lookup = %+v, %v", got, ok)
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
