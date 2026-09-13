package imagegen

import (
	"testing"
	"time"
)

// The picture a session just made is the one somebody opens next (the gallery's "Generated
// images (N)" points straight at its folder), so the decode is paid here, off the path of
// whoever is looking. The seam is the Agent's: this package must not know how to scale.
func TestStoreImagesWarmsTheThumbnail(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	warmed := make(chan string, 4)
	old := WarmThumb
	WarmThumb = func(p string) { warmed <- p }
	t.Cleanup(func() { WarmThumb = old })

	files, err := storeImages("sid-warm", []Image{{Bytes: []byte("not-a-real-png"), MIME: "image/png"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-warmed:
		if got != files[0].Path {
			t.Errorf("warmed %q, want the stored file %q", got, files[0].Path)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the stored image was never handed to the warm-up: the gallery pays the decode instead")
	}
}
