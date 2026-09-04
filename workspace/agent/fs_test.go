package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// TestSafeBrowsePath covers path resolution for the read-only file browser. Relative paths
// stay anchored on the browse root (denylist + traversal enforced); absolute paths — the
// form SendUserFile leaves for a file outside the browse root — are served only when they
// sit under an allowed read root (the browse root, the /tmp/claude-<uid> scratch base,
// or the role-scoped Agent Fleet docs mount), so a shared scratchpad or user guide opens.
func TestSafeBrowsePath(t *testing.T) {
	root := "/home/testuser"
	t.Setenv("AF_BROWSE_ROOT", root)

	scratch := scratchRoot() // /tmp/claude-<uid> — same base the harness uses for scratchpads

	cases := []struct {
		name, p  string
		wantFull string
		wantRel  string
		wantOK   bool
	}{
		// relative (browse-root-relative) — unchanged behavior
		{"rel under root", "repos/x/a.png", root + "/repos/x/a.png", "repos/x/a.png", true},
		{"rel root itself", "", root, "", true},
		{"rel traversal escapes", "../etc/passwd", "", "", false},
		{"rel denied", ".ssh/id_rsa", "", "", false},

		// absolute under the browse root → served, display path is home-relative
		{"abs under root", root + "/repos/x/a.png", root + "/repos/x/a.png", "repos/x/a.png", true},
		{"abs denied under root", root + "/.config/agent-fleet/store", "", "", false},

		// absolute under the scratch base → served, display path is the absolute path
		{"abs in scratch", scratch + "/sess/scratchpad/compact-preview.png", scratch + "/sess/scratchpad/compact-preview.png", scratch + "/sess/scratchpad/compact-preview.png", true},
		{"abs in staged docs", agentFleetDocsRoot() + "/guide/member/README.md", agentFleetDocsRoot() + "/guide/member/README.md", agentFleetDocsRoot() + "/guide/member/README.md", true},
		{"abs in codex generated images", codexGeneratedImagesRoot() + "/job/image.png", codexGeneratedImagesRoot() + "/job/image.png", codexGeneratedImagesRoot() + "/job/image.png", true},

		// absolute outside every allowed root → refused
		{"abs outside all", "/etc/passwd", "", "", false},
		{"abs scratch traversal escapes", scratch + "/../../etc/passwd", "", "", false},
	}
	for _, c := range cases {
		full, rel, ok := safeBrowsePath(c.p)
		if ok != c.wantOK {
			t.Errorf("%s: safeBrowsePath(%q) ok = %v, want %v", c.name, c.p, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if full != filepath.Clean(c.wantFull) || rel != c.wantRel {
			t.Errorf("%s: safeBrowsePath(%q) = (%q, %q), want (%q, %q)", c.name, c.p, full, rel, filepath.Clean(c.wantFull), c.wantRel)
		}
	}
}

func TestSafeWritableBrowsePathRefusesReadOnlyRoots(t *testing.T) {
	t.Setenv("AF_BROWSE_ROOT", t.TempDir())
	for _, p := range []string{scratchRoot() + "/note.txt", agentFleetDocsRoot() + "/guide/member/README.md"} {
		if _, _, ok := safeWritableBrowsePath(p); ok {
			t.Errorf("safeWritableBrowsePath(%q) unexpectedly allowed an absolute read-only path", p)
		}
	}
}

// TestHandleFSFileScratchpad drives the real HTTP handler end-to-end: a file written under
// the scratch base (where SendUserFile shares a compact preview) is served with its content,
// where before the fix the absolute /tmp path collapsed to $HOME/tmp/... and 404'd.
func TestHandleFSFileScratchpad(t *testing.T) {
	t.Setenv("AF_BROWSE_ROOT", t.TempDir()) // an unrelated home, to prove the file is served via the scratch root

	// A real file under scratchRoot(), the base the harness uses for per-session scratchpads.
	dir := filepath.Join(scratchRoot(), "aftest-"+t.Name())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	abs := filepath.Join(dir, "compact-preview.txt")
	const want = "shared scratch content"
	if err := os.WriteFile(abs, []byte(want), 0o600); err != nil {
		t.Fatalf("write scratch file: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/fs/file?path="+url.QueryEscape(abs), nil)
	handleFSFile(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rr.Body.String())
	}
	if resp.Content != want {
		t.Errorf("content = %q, want %q", resp.Content, want)
	}
	if resp.Path != abs {
		t.Errorf("path = %q, want %q (absolute, so the viewer round-trips the same key)", resp.Path, abs)
	}
}
