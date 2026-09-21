package main

// handleFSUpload's target-directory rule (ADR 0094 P3). The generation pane's reference-image
// picker uploads into `generated/console/inputs`, a folder no workspace has until something has
// been generated there — so "the directory must already exist" made the pane's own documented
// flow ("drop a file here to upload it into generated/console/inputs/") fail on any freshly
// started workspace, with nothing the member could do from the UI. Measured on the sandbox
// deployment before this: `400 not_dir`, surfaced as "could not upload" and a 0/3 counter.

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func uploadOnePNG(t *testing.T, path, name string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("not really a png, and this route does not care")); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/fs/upload?path="+path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	handleFSUpload(rec, req)
	return rec
}

// The pane's own case: the folder does not exist yet, and uploading has to create it rather than
// refuse. Same reasoning imagegen's resolveOutDir already states for out_dir — a folder named for
// work that has not happened yet cannot be expected to exist.
func TestFSUploadCreatesATargetThatDoesNotExistYet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", home)

	rec := uploadOnePNG(t, "generated/console/inputs", "input_sign.png")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	landed := filepath.Join(home, "generated", "console", "inputs", "input_sign.png")
	if _, err := os.Stat(landed); err != nil {
		t.Errorf("the picture is not at %s: %v", landed, err)
	}
}

// 🔴 The negative control, and the reason the change is "create when MISSING" rather than
// "create": a path that exists and is NOT a directory is still a mistake, and uploading "into" a
// file must not clobber it.
func TestFSUploadStillRefusesATargetThatIsAFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", home)
	occupied := filepath.Join(home, "generated")
	if err := os.WriteFile(occupied, []byte("i am a file"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := uploadOnePNG(t, "generated", "input_sign.png")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var doc struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Error.Code != "not_dir" {
		t.Errorf("code = %q, want not_dir", doc.Error.Code)
	}
	if b, err := os.ReadFile(occupied); err != nil || string(b) != "i am a file" {
		t.Errorf("the existing file was disturbed: %q %v", b, err)
	}
}

// Creating a missing folder must not become a way OUT of the browse root or into the denylist:
// the path goes through safeWritableBrowsePath first, exactly as it did before.
func TestFSUploadDoesNotCreateOutsideTheBrowseRootOrInDeniedPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", home)
	for _, p := range []string{"../escaped/inputs", ".ssh/inputs", ".config/agent-fleet/inputs"} {
		t.Run(p, func(t *testing.T) {
			if rec := uploadOnePNG(t, p, "x.png"); rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 for %s", rec.Code, p)
			}
			if _, err := os.Stat(filepath.Join(home, filepath.FromSlash(p))); err == nil {
				t.Errorf("%s was created anyway", p)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(home), "escaped")); err == nil {
		t.Error("a folder was created outside the browse root")
	}
}
