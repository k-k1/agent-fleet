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

// TestImageContentType checks that image extensions map to their MIME type (so the
// Console <img> preview renders them — SVG especially, which browsers won't sniff),
// case-insensitively, while non-images fall through to "" (served as octet-stream).
func TestImageContentType(t *testing.T) {
	cases := map[string]string{
		"a.svg":         "image/svg+xml",
		"DIAGRAM.SVG":   "image/svg+xml",
		"chart.png":     "image/png",
		"photo.JPEG":    "image/jpeg",
		"anim.gif":      "image/gif",
		"pic.webp":      "image/webp",
		"favicon.ico":   "image/x-icon",
		"report.md":     "",
		"notes.txt":     "",
		"archive.tar.gz": "",
		"noext":         "",
	}
	for name, want := range cases {
		if got := imageContentType(name); got != want {
			t.Errorf("imageContentType(%q) = %q, want %q", name, got, want)
		}
	}
}
