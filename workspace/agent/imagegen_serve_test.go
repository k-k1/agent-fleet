package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/imagegen"
)

// The whole Console side of ADR 0069 rests on one claim: a generated image sits under the
// browse root and outside every fsDeny prefix, so the mirror's existing FileCard can fetch it
// with no new route and no frontend change. That is the opposite of codex's own
// generated_images, which lives under the denied `.codex` and needs the narrow exception in
// fs.go — so it is worth holding down rather than assuming.
func TestGeneratedImageIsServableFromTheBrowseRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("AF_BROWSE_ROOT", root)

	dir := imagegen.GeneratedDir("sid-1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(dir, "image-1.png")
	if err := os.WriteFile(abs, []byte("PNG"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The path the transcript layer hands the Console. The mapping itself is
	// sessionx.toBrowseRel's (TestResolveUserFilesMapsAGeneratedImage); what matters here is
	// that the result is a path this handler will actually serve.
	rel := ".cache/agent-fleet/generated/sid-1/image-1.png"
	if got := filepath.Join(browseRoot(), rel); got != abs {
		t.Fatalf("the relative path this test asserts (%q) is not where the image was written (%q)", got, abs)
	}
	if isDenied(rel) {
		t.Fatalf("%q is denylisted — the card would show and never open", rel)
	}

	rr := httptest.NewRecorder()
	handleFSDownload(rr, httptest.NewRequest(http.MethodGet, "/api/fs/download?path="+url.QueryEscape(rel), nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "PNG" {
		t.Fatalf("download = %d %q, want 200 with the file", rr.Code, rr.Body.String())
	}
}
