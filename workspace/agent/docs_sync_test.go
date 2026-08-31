package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// docsTarGz builds a gzipped tar from name→content, so a test can hand the extractor
// exactly the archive it wants to defend against.
func docsTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(body)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSafeDocsEntryName(t *testing.T) {
	ok := []string{"use/README.md", "ref/agents.md", "./ref/agents.md"}
	for _, n := range ok {
		if _, good := safeDocsEntryName(n); !good {
			t.Errorf("%q should be accepted", n)
		}
	}
	bad := []string{"", "/etc/passwd", "../escape.md", "guide/../../escape.md", "..", "a\\b.md"}
	for _, n := range bad {
		if got, good := safeDocsEntryName(n); good {
			t.Errorf("%q should be refused, got %q", n, got)
		}
	}
}

// The archive arrives over the network into a fixed path: a traversal entry must abort
// the extraction, not write outside the destination.
func TestExtractDocsRefusesTraversal(t *testing.T) {
	dest := t.TempDir()
	outside := filepath.Join(filepath.Dir(dest), "escaped.md")
	body := docsTarGz(t, map[string]string{"../escaped.md": "pwned"})
	if _, err := extractDocsTarGz(bytes.NewReader(body), dest); err == nil {
		t.Fatal("expected an error for a parent-escaping entry")
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatalf("entry escaped the destination: %s exists", outside)
	}
}

func TestExtractDocsWritesTree(t *testing.T) {
	dest := t.TempDir()
	n, err := extractDocsTarGz(bytes.NewReader(docsTarGz(t, map[string]string{
		"use/README.ja.md":          "ガイド",
		"dev/04-workspace-agent.md": "dev",
	})), dest)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if n != 2 {
		t.Fatalf("files: want 2 got %d", n)
	}
	b, err := os.ReadFile(filepath.Join(dest, "use", "README.ja.md"))
	if err != nil || string(b) != "ガイド" {
		t.Fatalf("content: %q / %v", b, err)
	}
}

func TestExtractDocsRejectsGarbage(t *testing.T) {
	if _, err := extractDocsTarGz(bytes.NewReader([]byte("not a gzip stream")), t.TempDir()); err == nil {
		t.Fatal("expected an error for a non-gzip body")
	}
	// A truncated archive must fail rather than silently install a partial tree.
	full := docsTarGz(t, map[string]string{"use/README.md": "hello"})
	if _, err := extractDocsTarGz(bytes.NewReader(full[:len(full)-8]), t.TempDir()); err == nil {
		t.Fatal("expected an error for a truncated archive")
	}
}

// End to end against a stub CP: an empty docs root gets populated, and the staging dir
// is cleaned up afterwards.
func TestFetchWorkspaceDocs(t *testing.T) {
	body := docsTarGz(t, map[string]string{
		"use/README.md": "guide",
		"dev/04.md":     "dev",
	})
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/internal/docs" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	t.Setenv("AF_DOCS_TOKEN", "afd_test.tok")

	root := t.TempDir()
	n, err := fetchWorkspaceDocs(root)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if n != 2 {
		t.Fatalf("files: want 2 got %d", n)
	}
	if gotAuth != "Bearer afd_test.tok" {
		t.Fatalf("auth header: %q", gotAuth)
	}
	if b, err := os.ReadFile(filepath.Join(root, "use", "README.md")); err != nil || string(b) != "guide" {
		t.Fatalf("installed content: %q / %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(root, docsStageDir)); !os.IsNotExist(err) {
		t.Fatalf("staging dir must be removed, stat err=%v", err)
	}
	// Now that the root is populated it must count as provided — a later sync (or a
	// bind mount on docker/native) is never overwritten.
	if !docsRootPopulated(root) {
		t.Fatal("populated root should report populated")
	}
}

// No bridge env (docker / native / dev) → a quiet no-op, not an error the boot log
// complains about on every deployment that mounts its docs.
func TestFetchWorkspaceDocsBridgeOff(t *testing.T) {
	t.Setenv("AF_CP_BASE_URL", "")
	t.Setenv("AF_DOCS_TOKEN", "")
	if _, err := fetchWorkspaceDocs(t.TempDir()); err != errDocsBridgeOff {
		t.Fatalf("want errDocsBridgeOff got %v", err)
	}
}

// A mounted (non-empty) docs dir is authoritative: syncWorkspaceDocs must not touch it
// even when the bridge is configured and the CP would answer.
func TestSyncSkipsMountedDocs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "guide"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "guide", "README.md"), []byte("mounted"), 0o644); err != nil {
		t.Fatal(err)
	}
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	t.Setenv("AF_DOCS_TOKEN", "afd_test.tok")
	t.Setenv("AGENT_DOCS_DIR", root)

	syncWorkspaceDocs("test")
	if called {
		t.Fatal("a mounted docs dir must not trigger a pull")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "guide", "README.md")); string(b) != "mounted" {
		t.Fatalf("mounted content was modified: %q", b)
	}
}
