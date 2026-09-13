package filemeta

import "testing"

// TestImageContentType checks that image extensions map to their MIME type (so the
// Console <img> preview renders them — SVG especially, which browsers won't sniff),
// case-insensitively, while non-images fall through to "" (served as octet-stream).
func TestImageContentType(t *testing.T) {
	cases := map[string]string{
		"a.svg":          "image/svg+xml",
		"DIAGRAM.SVG":    "image/svg+xml",
		"chart.png":      "image/png",
		"photo.JPEG":     "image/jpeg",
		"anim.gif":       "image/gif",
		"pic.webp":       "image/webp",
		"favicon.ico":    "image/x-icon",
		"report.md":      "",
		"notes.txt":      "",
		"archive.tar.gz": "",
		"noext":          "",
	}
	for name, want := range cases {
		if got := ImageContentType(name); got != want {
			t.Errorf("ImageContentType(%q) = %q, want %q", name, got, want)
		}
	}
}
