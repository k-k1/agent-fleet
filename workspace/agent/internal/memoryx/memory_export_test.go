package memoryx

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// memoryProjectMemPath is the absolute path of a memory file on the live side (a test-side
// shorthand).
func memoryProjectMemPath(cfg, slug string, parts ...string) string {
	return filepath.Join(append([]string{cfg, "projects", slug, "memory"}, parts...)...)
}

// memoryTestAPI builds an isolated HOME and returns the real route table, token gate included.
func memoryTestAPI(t *testing.T) (http.Handler, string, string) {
	t.Helper()
	_, cfg, slug := memoryTestEnv(t)
	t.Setenv("AGENT_TOKEN", "smoke-token")
	return httpx.RequireToken(buildMux()), cfg, slug
}

// The export secret gate (★4, docs/log/39 resolution #2). A detection blocks by default
// and only an explicit ack lets it through. It has to stop at the API on its own, not at a UI
// confirmation dialog.
func TestMemoryExportBlocksSecretsUntilAcked(t *testing.T) {
	h, cfg, slug := memoryTestAPI(t)
	const fake = "AKIAQWERTYUIOPASDFGH"
	memoryWrite(t, memoryProjectMemPath(cfg, slug, "keys.md"), "deploy key "+fake+"\n")
	if w := smokeDo(t, h, "POST", "/agents/memory/snapshots", "smoke-token", ""); w.Code != http.StatusOK {
		t.Fatalf("seed snapshot: %d %s", w.Code, w.Body.String())
	}

	w := smokeDo(t, h, "GET", "/agents/memory/export?format=bundle", "smoke-token", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("export with a secret should be blocked: %d", w.Code)
	}
	var blocked struct {
		Error   struct{ Code string }
		Secrets []memorySecretFinding
	}
	if err := json.Unmarshal(w.Body.Bytes(), &blocked); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if blocked.Error.Code != errCodeMemorySecretDetected || len(blocked.Secrets) == 0 {
		t.Fatalf("blocked payload = %+v", blocked)
	}
	// The response must not carry the raw value: leaking it here would turn the defence into a
	// distribution channel.
	if strings.Contains(w.Body.String(), fake) {
		t.Fatal("the blocking response leaked the raw secret")
	}

	// With an ack it goes through as the user's own call. The body is a real git bundle.
	w = smokeDo(t, h, "GET", "/agents/memory/export?format=bundle&ack=1", "smoke-token", "")
	if w.Code != http.StatusOK {
		t.Fatalf("acked export: %d %s", w.Code, w.Body.String())
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, ".bundle") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if w.Header().Get("X-AF-Memory-Secrets") == "" {
		t.Error("acked export should still record that secrets were present")
	}
	if head := w.Body.String(); !strings.HasPrefix(head, "# v2 git bundle") && !strings.HasPrefix(head, "# v3 git bundle") {
		t.Fatalf("body is not a git bundle: %q", head[:min(32, len(head))])
	}

	// No temp file is left behind: a plaintext export must not stay on the mount.
	if ents, err := os.ReadDir(memoryWorkDir()); err == nil {
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "export-") {
				t.Errorf("export temp file left behind: %s", e.Name())
			}
		}
	}
}

// tar.gz export (latest only): it carries a manifest and keeps the repo paths as they are.
// Also checks that with no secret it passes without an ack, i.e. the gate does not block
// unconditionally.
func TestMemoryExportTar(t *testing.T) {
	h, _, slug := memoryTestAPI(t)
	if w := smokeDo(t, h, "POST", "/agents/memory/snapshots", "smoke-token", ""); w.Code != http.StatusOK {
		t.Fatalf("seed snapshot: %d %s", w.Code, w.Body.String())
	}
	w := smokeDo(t, h, "GET", "/agents/memory/export?format=tar", "smoke-token", "")
	if w.Code != http.StatusOK {
		t.Fatalf("tar export: %d %s", w.Code, w.Body.String())
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, ".tar.gz") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	names, blobs := memoryReadTarGz(t, w.Body.Bytes())
	for _, want := range []string{
		"manifest.json",
		"claude/projects/" + slug + "/memory/MEMORY.md",
		"codex/MEMORY.md",
	} {
		if _, ok := blobs[want]; !ok {
			t.Errorf("%q missing from the archive: %v", want, names)
		}
	}
	// The reverse of ★1: the export side must not capture anything collaterally either.
	for _, n := range names {
		if strings.Contains(n, ".jsonl") || strings.Contains(n, "credentials") || strings.Contains(n, ".git/") {
			t.Errorf("forbidden entry in the archive: %s", n)
		}
	}
	var m memoryExportManifest
	if err := json.Unmarshal(blobs["manifest.json"], &m); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if m.Format != "af-memory-tar" || m.Head == "" || m.Files != 5 {
		t.Errorf("manifest = %+v", m)
	}
	if _, err := time.Parse(time.RFC3339, m.GeneratedAt); err != nil {
		t.Errorf("manifest.generatedAt = %q", m.GeneratedAt)
	}
}

// Input validation, and the response when there is no history.
func TestMemoryExportBadInput(t *testing.T) {
	h, _, _ := memoryTestAPI(t)
	if w := smokeDo(t, h, "GET", "/agents/memory/export?format=zip", "smoke-token", ""); w.Code != http.StatusBadRequest {
		t.Errorf("unknown format: %d %s", w.Code, w.Body.String())
	}
	if w := smokeDo(t, h, "GET", "/agents/memory/export", "smoke-token", ""); w.Code != http.StatusNotFound {
		t.Errorf("export before any snapshot: %d %s", w.Code, w.Body.String())
	}
}

// memoryReadTarGz unpacks a tar.gz into its list of names and their contents.
func memoryReadTarGz(t *testing.T, b []byte) ([]string, map[string][]byte) {
	t.Helper()
	gr, err := gzip.NewReader(strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer gr.Close()
	names := []string{}
	blobs := map[string][]byte{}
	tr := tar.NewReader(gr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		names = append(names, h.Name)
		body, _ := io.ReadAll(tr)
		blobs[h.Name] = body
	}
	return names, blobs
}
