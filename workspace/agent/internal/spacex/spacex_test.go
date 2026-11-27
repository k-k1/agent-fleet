package spacex

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
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

// The filesystem root, in any spelling, is no space and must not panic: every session row asks.
func TestFolderOfRootSpellings(t *testing.T) {
	withHome(t)
	for _, d := range []string{"/", "/.", "/./", "//", "/.//.", "/..", "/../."} {
		if got := FolderOf(d); got != "" {
			t.Errorf("FolderOf(%q) = %q, want \"\"", d, got)
		}
	}
}

// A symlink under Root that points at a real space is refused the same way however the path is
// spelt: the last element is never followed, only the parent is resolved.
func TestSymlinkToASpaceIsRefusedInEverySpelling(t *testing.T) {
	withHome(t)
	sp, err := Create("notes")
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(Root(), "link-abcdef")
	if err := os.Symlink(sp.Dir, link); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{
		link,
		link + "/",
		link + "/.",
		link + "/./",
		Root() + "/./link-abcdef",
		Root() + "//link-abcdef",
		Root() + "/x/../link-abcdef",
	} {
		if strings.Contains(d, "/x/") {
			if err := os.MkdirAll(filepath.Join(Root(), "x"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if got := FolderOf(d); got != "" {
			t.Errorf("FolderOf(%q) = %q, want \"\" (a link is no space)", d, got)
		}
	}
	// The same spellings of the real folder still find it.
	for _, d := range []string{sp.Dir, sp.Dir + "/", sp.Dir + "/.", Root() + "/./" + sp.Folder} {
		if got := FolderOf(d); got != sp.Folder {
			t.Errorf("FolderOf(%q) = %q, want %q", d, got, sp.Folder)
		}
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

func TestCopyAttachmentKeepsTheMembersFileName(t *testing.T) {
	withHome(t)
	sp, err := Create("Reports")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, want string }{
		{"売上 2026.xlsx", "売上 2026.xlsx"}, // Unicode and spaces survive: the member reads this name
		{"../../etc/passwd", "passwd"},   // directory parts never reach the path
		{`C:\Users\me\notes.txt`, "notes.txt"},
		{"..", "file"}, // nothing usable
		{".hidden.md", "hidden.md"},
	} {
		got, err := CopyAttachment(sp.Folder, strings.NewReader("quarterly"), c.name)
		if err != nil {
			t.Fatalf("%q: %v", c.name, err)
		}
		if want := filepath.Join(sp.Dir, AttachmentsDir, c.want); got != want {
			t.Errorf("%q copied to %q, want %q", c.name, got, want)
		}
		if b, err := os.ReadFile(got); err != nil || string(b) != "quarterly" {
			t.Errorf("%q: copy = %q, %v", c.name, b, err)
		}
	}
}

func TestCopyAttachmentNeverOverwrites(t *testing.T) {
	withHome(t)
	sp, _ := Create("Reports")
	first, err := CopyAttachment(sp.Folder, strings.NewReader("new"), "report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := CopyAttachment(sp.Folder, strings.NewReader("new"), "report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	third, _ := CopyAttachment(sp.Folder, strings.NewReader("new"), "report.pdf")
	if filepath.Base(second) != "report-2.pdf" || filepath.Base(third) != "report-3.pdf" {
		t.Fatalf("second = %s, third = %s", second, third)
	}
	if b, _ := os.ReadFile(first); string(b) != "mine" {
		t.Fatalf("the first file was overwritten: %q", b)
	}
}

func TestCopyAttachmentCutsALongNameKeepingItsExtension(t *testing.T) {
	withHome(t)
	sp, _ := Create("Reports")
	got, err := CopyAttachment(sp.Folder, strings.NewReader("x"), strings.Repeat("あ", 300)+".pdf")
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Base(got)
	if n := len(base); n > attachmentNameMaxBytes || n < attachmentNameMaxBytes-3 || !strings.HasSuffix(base, ".pdf") || !utf8.ValidString(base) {
		t.Fatalf("name %q has %d bytes", base, n)
	}
}

// A multipart header can carry a file name far longer than any file system accepts; reducing it
// must not cost more than its length (a cut that re-encodes the rest per character is quadratic).
func TestAttachmentNameOfAnEnormousNameIsCheapAndStillValid(t *testing.T) {
	huge := strings.Repeat("あ", 1<<20) + ".tar.gz"
	start := time.Now()
	got := attachmentName(huge)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v for a %d byte name", d, len(huge))
	}
	if len(got) > attachmentNameMaxBytes || !utf8.ValidString(got) || !strings.HasSuffix(got, ".gz") {
		t.Fatalf("name = %q (%d bytes)", got, len(got))
	}
}

// An agent in the space can replace the attachments folder, the marker's folder or the space
// itself with a link; the copy never writes outside the space.
func TestCopyAttachmentNeverFollowsALinkOutOfTheSpace(t *testing.T) {
	withHome(t)
	outside := t.TempDir()
	empty := func(label string) {
		t.Helper()
		if ents, _ := os.ReadDir(outside); len(ents) != 0 {
			t.Fatalf("%s: the link target received %d file(s)", label, len(ents))
		}
	}
	t.Run("attachments folder", func(t *testing.T) {
		sp, _ := Create("A")
		if err := os.Symlink(outside, filepath.Join(sp.Dir, AttachmentsDir)); err != nil {
			t.Fatal(err)
		}
		if got, err := CopyAttachment(sp.Folder, strings.NewReader("x"), "a.txt"); err == nil {
			t.Fatalf("copied to %s through a symlinked folder", got)
		}
		empty("attachments")
	})
	t.Run("the space replaced by a link between check and copy", func(t *testing.T) {
		sp, _ := Create("B")
		if err := os.RemoveAll(sp.Dir); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, sp.Dir); err != nil {
			t.Fatal(err)
		}
		if got, err := CopyAttachment(sp.Folder, strings.NewReader("x"), "a.txt"); err == nil {
			t.Fatalf("copied to %s through a symlinked space", got)
		}
		empty("space")
	})
	t.Run("a link inside the attachments folder", func(t *testing.T) {
		sp, _ := Create("C")
		if err := os.Mkdir(filepath.Join(sp.Dir, AttachmentsDir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "target"), filepath.Join(sp.Dir, AttachmentsDir, "a.txt")); err != nil {
			t.Fatal(err)
		}
		got, err := CopyAttachment(sp.Folder, strings.NewReader("x"), "a.txt")
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(got) != "a-2.txt" {
			t.Fatalf("copied to %s, want a-2.txt next to the link", got)
		}
		empty("file link")
	})
}

func TestCopyAttachmentRefusesAFolderThatIsNoSpace(t *testing.T) {
	home := withHome(t)
	if err := os.MkdirAll(filepath.Join(home, "spaces", "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"plain", "missing", "../x", ""} {
		if got, err := CopyAttachment(f, strings.NewReader("x"), "a.txt"); err == nil {
			t.Errorf("%q: copied to %s", f, got)
		}
	}
}

// A rename changes the name the marker holds and nothing else: the folder, the creation time and
// the directory (so every session id and transcript path derived from it) stay as they are.
func TestRenameChangesOnlyTheDisplayName(t *testing.T) {
	withHome(t)
	sp, err := Create("Draft")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Rename(sp.Folder, "Q3 report")
	if err != nil {
		t.Fatal(err)
	}
	if got.Folder != sp.Folder || got.Dir != sp.Dir || got.CreatedAt != sp.CreatedAt || got.Name != "Q3 report" {
		t.Fatalf("Rename = %+v, was %+v", got, sp)
	}
	again, ok := Lookup(sp.Folder)
	if !ok || again.Name != "Q3 report" || again.CreatedAt != sp.CreatedAt {
		t.Fatalf("Lookup after rename = %+v, %v", again, ok)
	}
	if FolderOf(sp.Dir) != sp.Folder {
		t.Fatalf("the space stopped being a space: FolderOf = %q", FolderOf(sp.Dir))
	}
	// No temporary file is left beside the marker.
	ents, _ := os.ReadDir(sp.Dir)
	if len(ents) != 1 || ents[0].Name() != MarkerFile {
		t.Fatalf("space holds %v after a rename", ents)
	}
	// The empty name is allowed: the Console then shows the folder.
	if got, err := Rename(sp.Folder, ""); err != nil || got.Name != "" {
		t.Fatalf("Rename to empty = %+v, %v", got, err)
	}
}

func TestRenameKeepsTheFilesInTheSpace(t *testing.T) {
	withHome(t)
	sp, _ := Create("Draft")
	note := filepath.Join(sp.Dir, "notes.md")
	if err := os.WriteFile(note, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Rename(sp.Folder, "Other"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(note); err != nil || string(b) != "x" {
		t.Fatalf("the member's file changed: %q, %v", b, err)
	}
}

func TestRenameRefusesWhatIsNoSpace(t *testing.T) {
	home := withHome(t)
	if _, err := Rename("nope-abcdef", "x"); err != ErrNotFound {
		t.Fatalf("no ~/spaces at all: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(Root(), "plain-abcdef"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"plain-abcdef", "missing-abcdef", "../escape", "", ".."} {
		if _, err := Rename(f, "x"); err != ErrNotFound {
			t.Fatalf("Rename(%q) = %v, want ErrNotFound", f, err)
		}
	}
	// A symlink standing where a space folder should be is followed by nothing: the marker it
	// points at is another folder's, and it must stay as it is.
	outside := filepath.Join(home, "elsewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(outside, MarkerFile)
	if err := os.WriteFile(marker, []byte(`{"name":"theirs"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(Root(), "link-abcdef")); err != nil {
		t.Fatal(err)
	}
	if _, err := Rename("link-abcdef", "mine"); err != ErrNotFound {
		t.Fatalf("Rename through a folder symlink = %v", err)
	}
	if b, _ := os.ReadFile(marker); string(b) != `{"name":"theirs"}` {
		t.Fatalf("the marker outside the space was written: %s", b)
	}
}

// A symlink an agent put in the marker's place must not carry the write out of the space.
func TestRenameDoesNotFollowAMarkerSymlink(t *testing.T) {
	home := withHome(t)
	sp, _ := Create("Draft")
	target := filepath.Join(home, "victim.txt")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(sp.Dir, MarkerFile)
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, marker); err != nil {
		t.Fatal(err)
	}
	if _, err := Rename(sp.Folder, "x"); err != ErrNotFound {
		t.Fatalf("Rename with a symlinked marker = %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatalf("the symlink target was written: %q", b)
	}
}

// A marker that does not parse still marks the space; a rename gives it a name again.
func TestRenameRepairsAnUnparsableMarker(t *testing.T) {
	withHome(t)
	sp, _ := Create("Draft")
	if err := os.WriteFile(filepath.Join(sp.Dir, MarkerFile), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Rename(sp.Folder, "Fixed")
	if err != nil || got.Name != "Fixed" {
		t.Fatalf("Rename = %+v, %v", got, err)
	}
	if again, _ := Lookup(sp.Folder); again.Name != "Fixed" {
		t.Fatalf("Lookup = %+v", again)
	}
}

func TestRenameHandler(t *testing.T) {
	withHome(t)
	sp, _ := Create("Draft")
	call := func(folder, body string) *httptest.ResponseRecorder {
		mux := http.NewServeMux()
		mux.HandleFunc("PATCH /spaces/{folder}", HandleRename)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/spaces/"+folder, strings.NewReader(body)))
		return rec
	}
	rec := call(sp.Folder, `{"name":"  Renamed  "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body)
	}
	var out createResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Space.Name != "Renamed" || out.Space.Folder != sp.Folder {
		t.Fatalf("rename body %s: %v", rec.Body, err)
	}
	if rec := call(sp.Folder, `{"name":"a\u0007b"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_name") {
		t.Fatalf("control character: %d %s", rec.Code, rec.Body)
	}
	if rec := call(sp.Folder, `{"name":"`+strings.Repeat("x", NameMaxRunes+1)+`"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("too long: %d", rec.Code)
	}
	if rec := call("gone-abcdef", `{"name":"x"}`); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "space_not_found") {
		t.Fatalf("unknown space: %d %s", rec.Code, rec.Body)
	}
	// A refused rename leaves the name it had.
	if got, _ := Lookup(sp.Folder); got.Name != "Renamed" {
		t.Fatalf("Lookup = %+v", got)
	}
}

func markerText(t *testing.T, sp Space) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(sp.Dir, MarkerFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A relative symlink to another space stays inside Root, so os.Root follows it; Lookup does not
// count it as a space and neither may a rename: the other space keeps its name.
func TestRenameRefusesALinkToAnotherSpace(t *testing.T) {
	withHome(t)
	other, _ := Create("Theirs")
	if err := os.Symlink(other.Folder, filepath.Join(Root(), "alias-abcdef")); err != nil {
		t.Fatal(err)
	}
	if _, err := Rename("alias-abcdef", "Mine"); err != ErrNotFound {
		t.Fatalf("Rename through a link to another space = %v", err)
	}
	if got, _ := Lookup(other.Folder); got.Name != "Theirs" {
		t.Fatalf("the linked space was renamed: %+v", got)
	}
}

func TestRenameWhereTheFolderIsAFile(t *testing.T) {
	withHome(t)
	if err := os.MkdirAll(Root(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(Root(), "file-abcdef"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Rename("file-abcdef", "x"); err != ErrNotFound {
		t.Fatalf("Rename of a regular file = %v", err)
	}
}

func TestRenameRefusesAMarkerThatIsNotARegularFile(t *testing.T) {
	withHome(t)
	sp, _ := Create("Draft")
	marker := filepath.Join(sp.Dir, MarkerFile)
	other := filepath.Join(sp.Dir, "other.json")
	if err := os.WriteFile(other, []byte(`{"name":"elsewhere"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// A link to a file inside the space is followed by os.Root, and must not be read or replaced.
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("other.json", marker); err != nil {
		t.Fatal(err)
	}
	if _, err := Rename(sp.Folder, "x"); err != ErrNotFound {
		t.Fatalf("Rename with an in-space marker link = %v", err)
	}
	if b, _ := os.ReadFile(other); string(b) != `{"name":"elsewhere"}` {
		t.Fatalf("the link's target was written: %s", b)
	}
	// A FIFO neither blocks the rename nor is replaced.
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(marker, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := Rename(sp.Folder, "x"); done <- err }()
	select {
	case err := <-done:
		if err != ErrNotFound {
			t.Fatalf("Rename with a FIFO marker = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Rename hangs on a FIFO marker")
	}
}

// A marker that exists but cannot be read is no empty marker: nothing is written, the creation
// time stays, and the failure is not reported as a missing space.
func TestRenameDoesNotOverwriteAnUnreadableMarker(t *testing.T) {
	withHome(t)
	sp, _ := Create("Draft")
	marker := filepath.Join(sp.Dir, MarkerFile)
	before := markerText(t, sp)
	if err := os.Chmod(marker, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(marker, 0o644) })
	if f, err := os.Open(marker); err == nil {
		f.Close()
		t.Skip("running as a user that reads mode 000 files")
	}
	_, err := Rename(sp.Folder, "x")
	if err == nil || err == ErrNotFound || err == ErrChanged {
		t.Fatalf("Rename of an unreadable marker = %v", err)
	}
	_ = os.Chmod(marker, 0o644)
	if after := markerText(t, sp); after != before {
		t.Fatalf("the marker was overwritten: %q, was %q", after, before)
	}
}

// Changes made between the read and the publish: a removed space stays removed, a moved one is
// not written through its old path, a replaced marker is not overwritten by stale content.
func TestRenameAgainstChangesDuringTheWrite(t *testing.T) {
	setHook := func(t *testing.T, f func()) {
		prev := beforePublish
		beforePublish = f
		t.Cleanup(func() { beforePublish = prev })
	}
	t.Run("marker removed", func(t *testing.T) {
		withHome(t)
		sp, _ := Create("Draft")
		setHook(t, func() { _ = os.Remove(filepath.Join(sp.Dir, MarkerFile)) })
		if _, err := Rename(sp.Folder, "x"); err != ErrNotFound {
			t.Fatalf("Rename = %v", err)
		}
		if _, err := os.Lstat(filepath.Join(sp.Dir, MarkerFile)); err == nil {
			t.Fatal("the removed space was brought back")
		}
		ents, _ := os.ReadDir(sp.Dir)
		if len(ents) != 0 {
			t.Fatalf("a temporary file was left: %v", ents)
		}
	})
	t.Run("folder moved", func(t *testing.T) {
		withHome(t)
		sp, _ := Create("Draft")
		moved := filepath.Join(Root(), "moved-abcdef")
		before := markerText(t, sp)
		setHook(t, func() { _ = os.Rename(sp.Dir, moved) })
		if _, err := Rename(sp.Folder, "x"); err != ErrNotFound {
			t.Fatalf("Rename = %v", err)
		}
		if b, _ := os.ReadFile(filepath.Join(moved, MarkerFile)); string(b) != before {
			t.Fatalf("the moved folder's marker was written: %q", b)
		}
	})
	t.Run("marker replaced", func(t *testing.T) {
		withHome(t)
		sp, _ := Create("Draft")
		fresh := `{"name":"someone else","createdAt":"2020-01-01T00:00:00Z"}`
		setHook(t, func() {
			tmp := filepath.Join(sp.Dir, "swap.json")
			_ = os.WriteFile(tmp, []byte(fresh), 0o644)
			_ = os.Rename(tmp, filepath.Join(sp.Dir, MarkerFile))
		})
		if _, err := Rename(sp.Folder, "x"); err != ErrChanged {
			t.Fatalf("Rename = %v", err)
		}
		if got := markerText(t, sp); got != fresh {
			t.Fatalf("the replacement was overwritten: %q", got)
		}
	})
}

// Two renames at once each leave a whole marker: the creation time survives both.
func TestConcurrentRenamesKeepTheCreationTime(t *testing.T) {
	withHome(t)
	sp, _ := Create("Draft")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := Rename(sp.Folder, strings.Repeat("n", i+1)); err != nil {
				t.Errorf("Rename: %v", err)
			}
		}(i)
	}
	wg.Wait()
	got, ok := Lookup(sp.Folder)
	if !ok || got.CreatedAt != sp.CreatedAt || got.Name == "" {
		t.Fatalf("Lookup = %+v, %v", got, ok)
	}
}

func TestRenameHandlerNeedsAName(t *testing.T) {
	withHome(t)
	sp, _ := Create("Keep me")
	before := markerText(t, sp)
	for _, body := range []string{`{}`, `{"name":null}`, `null`, `{"other":"x"}`} {
		mux := http.NewServeMux()
		mux.HandleFunc("PATCH /spaces/{folder}", HandleRename)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/spaces/"+sp.Folder, strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if after := markerText(t, sp); after != before {
		t.Fatalf("a refused rename changed the marker: %q", after)
	}
}

// Go opens a directory root without O_DIRECTORY or O_NONBLOCK, so a FIFO where ~/spaces should
// be would hang a rename (holding renameMu) unless it is refused first.
func TestRenameWhereSpacesIsAFIFO(t *testing.T) {
	withHome(t)
	if err := syscall.Mkfifo(Root(), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := Rename("any-abcdef", "x"); done <- err }()
	select {
	case err := <-done:
		if err != ErrNotFound {
			t.Fatalf("Rename = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Rename hangs on a FIFO in the place of ~/spaces")
	}
}

func TestRenameAgainstChangesToTheMarkerAndTheRoot(t *testing.T) {
	setHook := func(t *testing.T, f func()) {
		prev := beforePublish
		beforePublish = f
		t.Cleanup(func() { beforePublish = prev })
	}
	t.Run("spaces moved away", func(t *testing.T) {
		home := withHome(t)
		sp, _ := Create("Draft")
		before := markerText(t, sp)
		old := filepath.Join(home, "spaces-old")
		setHook(t, func() { _ = os.Rename(Root(), old) })
		if _, err := Rename(sp.Folder, "x"); err != ErrNotFound {
			t.Fatalf("Rename = %v", err)
		}
		if b, _ := os.ReadFile(filepath.Join(old, sp.Folder, MarkerFile)); string(b) != before {
			t.Fatalf("the moved tree's marker was written: %q", b)
		}
	})
	t.Run("spaces replaced", func(t *testing.T) {
		home := withHome(t)
		sp, _ := Create("Draft")
		before := markerText(t, sp)
		old := filepath.Join(home, "spaces-old")
		setHook(t, func() {
			_ = os.Rename(Root(), old)
			_ = os.MkdirAll(Root(), 0o755)
		})
		if _, err := Rename(sp.Folder, "x"); err != ErrChanged {
			t.Fatalf("Rename = %v", err)
		}
		if b, _ := os.ReadFile(filepath.Join(old, sp.Folder, MarkerFile)); string(b) != before {
			t.Fatalf("the moved tree's marker was written: %q", b)
		}
	})
	t.Run("marker rewritten in place", func(t *testing.T) {
		withHome(t)
		sp, _ := Create("Draft")
		theirs := `{"name":"someone else","createdAt":"2019-05-05T00:00:00Z","extra":"a longer marker"}`
		setHook(t, func() {
			// The same inode, as an editor or an agent that saves over the file does.
			_ = os.WriteFile(filepath.Join(sp.Dir, MarkerFile), []byte(theirs), 0o644)
		})
		if _, err := Rename(sp.Folder, "x"); err != ErrChanged {
			t.Fatalf("Rename = %v", err)
		}
		if got := markerText(t, sp); got != theirs {
			t.Fatalf("the in-place update was overwritten: %q", got)
		}
	})
}

// A marker over the size limit cannot be told from a valid one: it stays as it is, with its
// creation time, rather than being "repaired".
func TestRenameLeavesAnOversizedMarkerAlone(t *testing.T) {
	withHome(t)
	sp, _ := Create("Draft")
	big := `{"name":"Draft","createdAt":"2021-02-03T04:05:06Z"}` + strings.Repeat(" ", markerMaxBytes)
	if err := os.WriteFile(filepath.Join(sp.Dir, MarkerFile), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Rename(sp.Folder, "x"); err != ErrMarkerTooBig {
		t.Fatalf("Rename = %v", err)
	}
	if got := markerText(t, sp); got != big {
		t.Fatalf("the marker was rewritten (%d bytes)", len(got))
	}
}

func TestRenameHandlerMapsConflictAndFailure(t *testing.T) {
	withHome(t)
	sp, _ := Create("Draft")
	call := func() *httptest.ResponseRecorder {
		mux := http.NewServeMux()
		mux.HandleFunc("PATCH /spaces/{folder}", HandleRename)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/spaces/"+sp.Folder, strings.NewReader(`{"name":"x"}`)))
		return rec
	}
	prev := beforePublish
	t.Cleanup(func() { beforePublish = prev })
	beforePublish = func() {
		_ = os.WriteFile(filepath.Join(sp.Dir, MarkerFile), []byte(`{"name":"theirs-with-more-bytes"}`), 0o644)
	}
	if rec := call(); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "space_changed") {
		t.Fatalf("changed: %d %s", rec.Code, rec.Body)
	}
	beforePublish = prev
	big := strings.Repeat(" ", markerMaxBytes+1)
	if err := os.WriteFile(filepath.Join(sp.Dir, MarkerFile), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := call(); rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "rename_failed") {
		t.Fatalf("too big: %d %s", rec.Code, rec.Body)
	}
}

// Only the name is the rename's: any other field of a readable marker survives it.
func TestRenameKeepsTheMarkersOtherFields(t *testing.T) {
	withHome(t)
	sp, _ := Create("Draft")
	orig := `{"name":"Draft","createdAt":"2021-02-03T04:05:06Z","owner":"someone","tags":["a","b"],"n":1}`
	if err := os.WriteFile(filepath.Join(sp.Dir, MarkerFile), []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Rename(sp.Folder, "New name")
	if err != nil || got.CreatedAt != "2021-02-03T04:05:06Z" {
		t.Fatalf("Rename = %+v, %v", got, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(markerText(t, sp)), &fields); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"name": `"New name"`, "createdAt": `"2021-02-03T04:05:06Z"`, "owner": `"someone"`, "tags": `["a","b"]`, "n": `1`} {
		if string(fields[k]) != want {
			t.Fatalf("field %q = %s, want %s (marker %s)", k, fields[k], want, markerText(t, sp))
		}
	}
}

// A writer that rewrites the marker in place with a body of the same length and puts the times
// back changes neither the inode, the size nor the modification time: only the content shows it.
func TestRenameSeesAContentChangeThatKeepsSizeAndTimes(t *testing.T) {
	withHome(t)
	sp, _ := Create("Draft")
	marker := filepath.Join(sp.Dir, MarkerFile)
	orig := `{"name":"Draft","createdAt":"2021-02-03T04:05:06Z"}`
	if err := os.WriteFile(marker, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(marker)
	theirs := `{"name":"Draft","createdAt":"2022-02-03T04:05:06Z"}`
	if len(theirs) != len(orig) {
		t.Fatal("test bodies differ in length")
	}
	prev := beforePublish
	beforePublish = func() {
		_ = os.WriteFile(marker, []byte(theirs), 0o644)
		_ = os.Chtimes(marker, fi.ModTime(), fi.ModTime())
	}
	t.Cleanup(func() { beforePublish = prev })
	if _, err := Rename(sp.Folder, "x"); err != ErrChanged {
		t.Fatalf("Rename = %v", err)
	}
	if got := markerText(t, sp); got != theirs {
		t.Fatalf("the rewrite was overwritten: %q", got)
	}
}

// What is opened must be the file that was looked at: a regular file with another identity (a
// link followed inside the space after the Lstat) is refused before it is read.
func TestReadMarkerBytesRefusesAnotherFile(t *testing.T) {
	withHome(t)
	sp, _ := Create("Draft")
	other := filepath.Join(sp.Dir, "other.json")
	if err := os.WriteFile(other, []byte(`{"name":"elsewhere"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	otherFi, err := os.Lstat(other)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(sp.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, _, err := readMarkerBytes(root, otherFi); err != ErrChanged {
		t.Fatalf("readMarkerBytes with another file's identity = %v", err)
	}
	markFi, _ := os.Lstat(filepath.Join(sp.Dir, MarkerFile))
	if b, _, err := readMarkerBytes(root, markFi); err != nil || !strings.Contains(string(b), "Draft") {
		t.Fatalf("readMarkerBytes = %q, %v", b, err)
	}
}

// What a rename writes is read back by the same limit: a marker that would end up over it is
// refused with the old one left, and one that fits exactly is read whole afterwards.
func TestRenameOutputStaysWithinWhatLookupReads(t *testing.T) {
	withHome(t)
	sp, _ := Create("Draft")
	marker := filepath.Join(sp.Dir, MarkerFile)
	// A marker of exactly the limit (with its newline) holding the 5-letter name "Draft".
	build := func(pad int) string {
		return `{"name":"Draft","createdAt":"2021-02-03T04:05:06Z","pad":"` + strings.Repeat("x", pad) + `"}` + "\n"
	}
	pad := markerMaxBytes - len(build(0))
	orig := build(pad)
	if len(orig) != markerMaxBytes {
		t.Fatalf("test marker is %d bytes", len(orig))
	}
	if err := os.WriteFile(marker, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := Lookup(sp.Folder); !ok || got.Name != "Draft" {
		t.Fatalf("a marker of the limit is not readable: %+v", got)
	}
	// "Updated" is 2 bytes longer than "Draft": over the limit, refused, the old bytes stay.
	if _, err := Rename(sp.Folder, "Updated"); err != ErrMarkerTooBig {
		t.Fatalf("Rename to a longer name = %v", err)
	}
	if got := markerText(t, sp); got != orig {
		t.Fatalf("the marker changed on a refused rename (%d bytes)", len(got))
	}
	ents, _ := os.ReadDir(sp.Dir)
	if len(ents) != 1 {
		t.Fatalf("a temporary file was left: %v", ents)
	}
	// The same size is written and read back.
	if _, err := Rename(sp.Folder, "Drift"); err != nil {
		t.Fatalf("Rename to a name of the same length = %v", err)
	}
	if got, ok := Lookup(sp.Folder); !ok || got.Name != "Drift" || got.CreatedAt != "2021-02-03T04:05:06Z" {
		t.Fatalf("Lookup after the rename = %+v, %v", got, ok)
	}
}

// A createdAt of another type makes the readers drop the whole marker (name included), so the
// rename does not keep it: the new name must be readable right after.
func TestRenameDropsACreatedAtTheReadersCannotDecode(t *testing.T) {
	withHome(t)
	sp, _ := Create("Draft")
	if err := os.WriteFile(filepath.Join(sp.Dir, MarkerFile), []byte(`{"name":"Draft","createdAt":12345,"keep":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Rename(sp.Folder, "Updated"); err != nil {
		t.Fatal(err)
	}
	got, ok := Lookup(sp.Folder)
	if !ok || got.Name != "Updated" {
		t.Fatalf("Lookup after the rename = %+v, %v", got, ok)
	}
	if !strings.Contains(markerText(t, sp), `"keep":true`) {
		t.Fatalf("another field was lost: %s", markerText(t, sp))
	}
}

// encoding/json matches keys without regard to case, so a hand-written marker may spell them
// otherwise; the rename treats every spelling the way the readers do, and what it answers is what
// Lookup reads next.
func TestRenameAgreesWithLookupOnKeySpellings(t *testing.T) {
	for name, orig := range map[string]string{
		"numeric CreatedAt":    `{"name":"Draft","CreatedAt":12345,"keep":1}`,
		"numeric Name":         `{"Name":7,"createdAt":"2021-02-03T04:05:06Z","keep":1}`,
		"upper-case CreatedAt": `{"Name":"Draft","CREATEDAT":"2021-02-03T04:05:06Z","keep":1}`,
		"two spellings":        `{"name":"Draft","createdat":9,"CreatedAt":"2021-02-03T04:05:06Z","keep":1}`,
		"canonical and typed":  `{"name":"Draft","createdAt":"2021-02-03T04:05:06Z","keep":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			withHome(t)
			sp, _ := Create("Draft")
			if err := os.WriteFile(filepath.Join(sp.Dir, MarkerFile), []byte(orig), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := Rename(sp.Folder, "Updated")
			if err != nil {
				t.Fatal(err)
			}
			read, ok := Lookup(sp.Folder)
			if !ok || read.Name != "Updated" || got.Name != read.Name || got.CreatedAt != read.CreatedAt {
				t.Fatalf("Rename answered %+v, Lookup reads %+v, %v (marker %s)", got, read, ok, markerText(t, sp))
			}
			if !strings.Contains(markerText(t, sp), `"keep":1`) {
				t.Fatalf("another field was lost: %s", markerText(t, sp))
			}
		})
	}
}

func TestMoveOutAndInKeepTheWholeFolder(t *testing.T) {
	home := withHome(t)
	sp, err := Create("Notes")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sp.Dir, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sp.Dir, "a", "b", "f.txt"), []byte("body"), 0o640); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(home, "trash", "x.space")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := moveOutPath(sp.Folder, dest); err != nil {
		t.Fatal(err)
	}
	if _, ok := Lookup(sp.Folder); ok {
		t.Fatal("the moved space is still a space")
	}
	if err := moveOutPath(sp.Folder, dest+"2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("moving a space that is gone = %v, want ErrNotFound", err)
	}
	// Something already at the folder's name wins, an empty directory included (a rename would
	// replace it).
	if err := os.MkdirAll(sp.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := moveInPath(dest, sp.Folder); !errors.Is(err, ErrExists) {
		t.Fatalf("MoveIn onto an empty directory = %v, want ErrExists", err)
	}
	if err := os.Remove(sp.Dir); err != nil {
		t.Fatal(err)
	}
	if err := moveInPath(dest, sp.Folder); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(sp.Dir, "a", "b", "f.txt"))
	if err != nil || fi.Mode().Perm() != 0o640 {
		t.Fatalf("file after the round trip: %v %v", fi, err)
	}
	if got, ok := Lookup(sp.Folder); !ok || got.Name != "Notes" {
		t.Fatalf("Lookup = %+v %v, want the space back under its name", got, ok)
	}
}

func TestMoveOutRefusesWhatIsNoSpace(t *testing.T) {
	home := withHome(t)
	if err := os.MkdirAll(filepath.Join(home, "spaces", "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	real, err := Create("Real")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real.Dir, filepath.Join(home, "spaces", "link")); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(home, "x.space")
	for _, folder := range []string{"plain", "link", "../repos", "", "nope"} {
		if err := moveOutPath(folder, dest); !errors.Is(err, ErrNotFound) {
			t.Errorf("MoveOut(%q) = %v, want ErrNotFound", folder, err)
		}
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("something was moved")
	}
	// A trash entry that lost its marker is not turned into a space.
	src := filepath.Join(home, "entry.space")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := moveInPath(src, "back-abc123"); !errors.Is(err, ErrNotFound) {
		t.Errorf("MoveIn of an unmarked folder = %v, want ErrNotFound", err)
	}
}

// A write inside a space and the move of the whole folder never overlap: the move waits for the
// copy (or rename) in flight, so a path handed out for the copy is not left in the trash.
func TestMoveOutWaitsForWritesInTheSpace(t *testing.T) {
	home := withHome(t)
	sp, err := Create("Busy")
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(home, "x.space")

	release := make(chan struct{})
	started := make(chan struct{})
	copied := make(chan string, 1)
	go func() {
		p, err := CopyAttachment(sp.Folder, blockingReader{started: started, release: release, data: "payload"}, "f.txt")
		if err != nil {
			t.Error(err)
		}
		copied <- p
	}()
	<-started
	moved := make(chan error, 1)
	go func() { moved <- moveOutPath(sp.Folder, dest) }()
	select {
	case err := <-moved:
		t.Fatalf("the move did not wait for the copy in flight: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	if p := <-copied; filepath.Dir(filepath.Dir(p)) != sp.Dir {
		t.Fatalf("copy landed at %q", p)
	}
	if err := <-moved; err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "attachments", "f.txt")); err != nil || string(b) != "payload" {
		t.Fatalf("the copy is not in the moved folder: %q %v", b, err)
	}
}

func TestMoveOutWaitsForARename(t *testing.T) {
	home := withHome(t)
	sp, err := Create("Before")
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(home, "x.space")
	reached := make(chan struct{})
	release := make(chan struct{})
	beforePublish = func() {
		close(reached)
		<-release
	}
	t.Cleanup(func() { beforePublish = func() {} })
	renamed := make(chan error, 1)
	go func() {
		_, err := Rename(sp.Folder, "After")
		renamed <- err
	}()
	<-reached
	moved := make(chan error, 1)
	go func() { moved <- moveOutPath(sp.Folder, dest) }()
	select {
	case err := <-moved:
		t.Fatalf("the move did not wait for the rename: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	if err := <-renamed; err != nil {
		t.Fatal(err)
	}
	if err := <-moved; err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, MarkerFile)); !strings.Contains(string(b), "After") {
		t.Fatalf("the moved marker = %q, want the new name", b)
	}
}

// blockingReader reads nothing until release is closed, and says when it was first read.
type blockingReader struct {
	started chan struct{}
	release chan struct{}
	data    string
}

func (b blockingReader) Read(p []byte) (int, error) {
	select {
	case <-b.started:
	default:
		close(b.started)
	}
	<-b.release
	return copy(p, b.data), io.EOF
}

func moveOutPath(folder, dest string) error {
	trash, err := os.OpenRoot(filepath.Dir(dest))
	if err != nil {
		return err
	}
	defer trash.Close()
	return MoveOut(folder, trash, filepath.Base(dest))
}

func moveInPath(src, folder string) error {
	trash, err := os.OpenRoot(filepath.Dir(src))
	if err != nil {
		return err
	}
	defer trash.Close()
	entry := filepath.Base(src)
	identity, err := ReadTrashIdentity(trash, entry)
	if err != nil {
		return err
	}
	spaces, err := os.OpenRoot(Root())
	if err != nil {
		return err
	}
	defer spaces.Close()
	return MoveIn(trash, entry, spaces, folder, identity)
}
