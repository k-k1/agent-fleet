package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func postMemoImageGC(t *testing.T, keep []string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"keep": keep})
	rec := httptest.NewRecorder()
	handleMemoImageGC(rec, httptest.NewRequest(http.MethodPost, "/memos/images/gc", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("gc status = %d, body %s", rec.Code, rec.Body)
	}
	var out struct {
		Removed int `json:"removed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("gc body: %v", err)
	}
	return out.Removed
}

// TestMemoImageGCSparesYoungFiles pins the grace period: a sweep whose keep list does not
// name a file still leaves it when it was stored less than memoImageGCGrace ago, and removes
// it once it is older.
func TestMemoImageGCSparesYoungFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := memoImagesDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-memoImageGCGrace - time.Minute)
	write := func(name string, mtime time.Time) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	write("paste-1.png", old)        // orphan past the grace: removed
	write("paste-2.png", old)        // named by the keep list: kept
	write("paste-3.png", time.Now()) // orphan inside the grace: kept
	write(".paste-tmp", old)         // an upload's temp file: never touched
	write("notes.txt", old)          // not a paste file: never touched

	if got := postMemoImageGC(t, []string{"paste-2.png"}); got != 1 {
		t.Errorf("removed = %d, want 1", got)
	}
	for name, want := range map[string]bool{
		"paste-1.png": false,
		"paste-2.png": true,
		"paste-3.png": true,
		".paste-tmp":  true,
		"notes.txt":   true,
	} {
		_, err := os.Stat(filepath.Join(dir, name))
		if got := err == nil; got != want {
			t.Errorf("%s present = %v, want %v", name, got, want)
		}
	}
}

// TestMemoImageGCKeepsUploadItsClientHasNotSeen replays #4827: an upload is stored, and a
// sweep from a client that has not read its response yet (so its keep list cannot name the
// file) runs before that response arrives. The file must survive for the composer to show.
func TestMemoImageGCKeepsUploadItsClientHasNotSeen(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "shot.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte("\x89PNG\r\n\x1a\n"))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/memos/paste-image", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	handleMemoPasteImage(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, body %s", rec.Code, rec.Body)
	}
	var up struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &up); err != nil || !strings.HasPrefix(up.Name, "paste-") {
		t.Fatalf("upload body %s: %v", rec.Body, err)
	}

	// The sweep runs with the response still unread: nothing in the keep list names it.
	if got := postMemoImageGC(t, nil); got != 0 {
		t.Errorf("removed = %d, want 0", got)
	}
	if _, err := os.Stat(up.Path); err != nil {
		t.Fatalf("upload swept before its client saw it: %v", err)
	}
}
