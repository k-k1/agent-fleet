package fsx

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/spacex"
)

// withSpace points home and the browse root at a temporary directory and makes one space there.
func withSpace(t *testing.T) (home string, sp spacex.Space) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_BROWSE_ROOT", home)
	sp, err := spacex.Create("Draft")
	if err != nil {
		t.Fatal(err)
	}
	return home, sp
}

func errCode(t *testing.T, body []byte) string {
	t.Helper()
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &resp)
	return resp.Error.Code
}

// The file browser refuses to delete or rename ~/spaces, a space's folder or its marker
// (#5231): each of those is the space delete's job, and a renamed folder breaks --resume.
func TestSpaceFolderAndMarkerCannotBeDeletedOrRenamed(t *testing.T) {
	home, sp := withSpace(t)
	if err := os.Symlink(filepath.Join(home, "spaces"), filepath.Join(home, "alias")); err != nil {
		t.Fatal(err)
	}
	rel := "spaces/" + sp.Folder
	for _, p := range []string{"spaces", rel, rel + "/" + spacex.MarkerFile, "alias/" + sp.Folder} {
		rec := fsCall(HandleDelete, "/fs/delete?path="+p)
		if rec.Code != http.StatusForbidden || errCode(t, rec.Body.Bytes()) != spaceProtectedCode {
			t.Errorf("delete %s = %d %s, want 403 %s", p, rec.Code, rec.Body.String(), spaceProtectedCode)
		}
		rec = fsCall(HandleRename, "/fs/rename?from="+p+"&to=moved")
		if rec.Code != http.StatusForbidden || errCode(t, rec.Body.Bytes()) != spaceProtectedCode {
			t.Errorf("rename %s = %d %s, want 403 %s", p, rec.Code, rec.Body.String(), spaceProtectedCode)
		}
	}
	if _, ok := spacex.Lookup(sp.Folder); !ok {
		t.Fatal("the space is gone")
	}
	if _, err := os.Lstat(filepath.Join(sp.Dir, spacex.MarkerFile)); err != nil {
		t.Fatalf("the marker is gone: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home, "moved")); err == nil {
		t.Fatal("something was renamed")
	}
}

// Files inside a space stay the member's: they are renamed and deleted as anywhere else.
func TestFilesInsideASpaceStayEditable(t *testing.T) {
	_, sp := withSpace(t)
	rel := "spaces/" + sp.Folder
	if err := os.WriteFile(filepath.Join(sp.Dir, "notes.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(sp.Dir, "drafts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if rec := fsCall(HandleRename, "/fs/rename?from="+rel+"/notes.md&to="+rel+"/notes2.md"); rec.Code != http.StatusOK {
		t.Fatalf("rename inside a space = %d %s", rec.Code, rec.Body.String())
	}
	if rec := fsCall(HandleDelete, "/fs/delete?path="+rel+"/drafts"); rec.Code != http.StatusOK {
		t.Fatalf("delete inside a space = %d %s", rec.Code, rec.Body.String())
	}
}

// A folder made by hand under ~/spaces is no space and can be deleted.
func TestFolderMadeByHandUnderSpacesCanBeDeleted(t *testing.T) {
	home, _ := withSpace(t)
	if err := os.Mkdir(filepath.Join(home, "spaces", "by-hand"), 0o755); err != nil {
		t.Fatal(err)
	}
	if rec := fsCall(HandleDelete, "/fs/delete?path=spaces/by-hand"); rec.Code != http.StatusOK {
		t.Fatalf("delete of a folder made by hand = %d %s", rec.Code, rec.Body.String())
	}
}

// The tree leaves a space's marker out; the same file name elsewhere is listed.
func TestTreeHidesTheSpaceMarker(t *testing.T) {
	home, sp := withSpace(t)
	if err := os.WriteFile(filepath.Join(sp.Dir, "notes.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := treeEntries(t, "path=spaces/"+sp.Folder)
	if _, shown := got[spacex.MarkerFile]; shown {
		t.Fatal("the tree lists the space's marker")
	}
	if _, ok := got["notes.md"]; !ok {
		t.Fatalf("the tree lost the space's files: %v", got)
	}
	other := filepath.Join(home, "elsewhere")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, spacex.MarkerFile), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := treeEntries(t, "path=elsewhere")[spacex.MarkerFile]; !ok {
		t.Fatal("a file of the same name outside ~/spaces was hidden")
	}
}
