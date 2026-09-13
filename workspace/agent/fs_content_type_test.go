package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestFSDownloadSVGContentType exercises the real download handler: an .svg under the
// browse root must be served as image/svg+xml (not octet-stream), confirming
// http.ServeContent honors the Content-Type we set rather than overriding it.
func TestFSDownloadSVGContentType(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	if err := os.WriteFile(filepath.Join(root, "d.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/fs/download?path=d.svg", nil)
	handleFSDownload(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("Content-Type = %q, want image/svg+xml", ct)
	}
}

// The extension table this handler reads is exercised in internal/filemeta.
