package main

// Role-scoped docs, pulled from the CP when nothing was mounted (docs/build/04 §4.9).
//
// The agent-fleet docs under /usr/local/share/agent-fleet/docs are what the Console's
// user guide opens and what the in-container agents grep to answer questions about this
// environment. They are NOT baked into the workspace image on purpose: a shared image
// cannot enforce "a member must not read the internal docs", so the CP bakes them and
// hands each container only the subset its member's role may see.
//
// On docker / native that handover is a read-only bind mount, and this file does
// nothing. On ECS there is no host path to mount from — the task runs on Fargate or an
// EC2 instance the CP never touches — so the directory stayed EMPTY and the guide
// opened nothing. Here the container pulls the same role-scoped subset over the CP's
// internal bridge instead (control-plane/docs_bridge.go).
//
// Three properties this file exists to guarantee:
//
//   - **a mount always wins.** A non-empty docs dir means the runtime already provided
//     them; we never write over a mount (and on native rootfs mode we could not — / is
//     read-only). The pull is the fallback, not a second source of truth.
//   - **the archive is not trusted.** It arrives over the network into a fixed path, so
//     every entry is checked: regular files only, no absolute or parent-escaping names,
//     and hard caps on count and total size.
//   - **partial output never becomes the guide.** Extraction lands in a staging dir and
//     each top-level subtree is renamed into place only after the whole archive read
//     cleanly, so a dropped connection leaves the previous state rather than half a doc.

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	// docsMaxBytes / docsMaxFiles bound what one pull may write. The real tree is a few
	// MB and a few hundred files even for super_admin (whose subset is the whole thing);
	// these are a backstop against a broken or hostile stream filling the container disk.
	docsMaxBytes = 128 << 20
	docsMaxFiles = 20000
	// docsStageDir is the extraction staging dir, kept INSIDE the docs root so the
	// rename into place stays on one filesystem.
	docsStageDir = ".af-docs-stage"
)

// errDocsBridgeOff reports that this container has no docs bridge — the CP injects
// AF_CP_BASE_URL / AF_DOCS_TOKEN only when it has a public base URL. Normal on a
// single-node dev deployment, not a failure.
var errDocsBridgeOff = errors.New("docs bridge is not configured in this deployment")

// syncWorkspaceDocs fetches and installs the docs when the runtime did not mount any.
// Best-effort: every failure leaves the directory exactly as it was.
func syncWorkspaceDocs(why string) {
	root := agentFleetDocsRoot()
	if docsRootPopulated(root) {
		return // mounted by the runtime (docker / native) — nothing to do
	}
	n, err := fetchWorkspaceDocs(root)
	switch {
	case errors.Is(err, errDocsBridgeOff):
		return // not configured here; stay quiet
	case err != nil:
		// Worth one line: without it, "the user guide does not open" has no trace anywhere.
		log.Printf("docs sync (%s): %v (docs stay unavailable in this container)", why, err)
	default:
		log.Printf("docs sync (%s): installed %d file(s) into %s", why, n, root)
	}
}

// docsRootPopulated reports whether the docs root already holds anything (ignoring a
// leftover staging dir from a killed extraction).
func docsRootPopulated(root string) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Name() != docsStageDir {
			return true
		}
	}
	return false
}

// fetchWorkspaceDocs pulls the tar.gz from the CP and installs it under root.
func fetchWorkspaceDocs(root string) (int, error) {
	base := strings.TrimRight(os.Getenv("AF_CP_BASE_URL"), "/")
	token := os.Getenv("AF_DOCS_TOKEN")
	if base == "" || token == "" {
		return 0, errDocsBridgeOff
	}
	// The docs root is baked into the image (empty) and owned by the container user, but
	// a read-only rootfs or a stray mount can still make it unwritable — find out before
	// spending a download on it.
	stage := filepath.Join(root, docsStageDir)
	if err := os.RemoveAll(stage); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return 0, fmt.Errorf("docs dir is not writable: %w", err)
	}
	defer os.RemoveAll(stage)

	req, err := http.NewRequest(http.MethodGet, base+"/internal/docs", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return 0, fmt.Errorf("CP docs API error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	n, err := extractDocsTarGz(resp.Body, stage)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil // deployment stages nothing for this role; leave the dir empty
	}
	// Whole archive read cleanly → publish. Top-level subtrees only ("guide", "dev", …),
	// so a member sees a complete subtree or none of it.
	entries, err := os.ReadDir(stage)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		dest := filepath.Join(root, e.Name())
		if err := os.RemoveAll(dest); err != nil {
			return 0, err
		}
		if err := os.Rename(filepath.Join(stage, e.Name()), dest); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// extractDocsTarGz unpacks the stream into dest, refusing anything that is not a plain
// file at a plain relative path. Returns the number of files written.
func extractDocsTarGz(r io.Reader, dest string) (int, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return 0, fmt.Errorf("docs archive is not gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var files int
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return files, fmt.Errorf("docs archive is truncated or corrupt: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue // dirs are implied by the file paths; links/devices are refused outright
		}
		rel, ok := safeDocsEntryName(hdr.Name)
		if !ok {
			return files, fmt.Errorf("docs archive contains an unsafe path: %q", hdr.Name)
		}
		if files++; files > docsMaxFiles {
			return files, fmt.Errorf("docs archive has more than %d files", docsMaxFiles)
		}
		if total += hdr.Size; total > docsMaxBytes {
			return files, fmt.Errorf("docs archive exceeds %d bytes", int64(docsMaxBytes))
		}
		full := filepath.Join(dest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return files, err
		}
		f, err := os.OpenFile(full, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return files, err
		}
		// LimitReader by the header size: a header that lies about a short size must not
		// let the body keep writing past the budget we just accounted for.
		_, cErr := io.Copy(f, io.LimitReader(tr, hdr.Size))
		if err := f.Close(); err != nil && cErr == nil {
			cErr = err
		}
		if cErr != nil {
			return files, cErr
		}
	}
	// tar stops at the end-of-archive marker, which a stream that was cut short can still
	// contain — the gzip CRC/length trailer is the only thing that proves the whole body
	// arrived. Read the remainder so a truncated download fails here rather than being
	// published as a complete docs tree.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return files, fmt.Errorf("docs archive is truncated or corrupt: %w", err)
	}
	return files, nil
}

// safeDocsEntryName accepts only a relative, cleaned, forward-slash path that stays
// inside the destination — "use/README.md", never "/etc/x" or "../../x".
func safeDocsEntryName(name string) (string, bool) {
	n := strings.TrimSpace(name)
	if n == "" || strings.ContainsRune(n, '\\') || strings.ContainsRune(n, 0) {
		return "", false
	}
	if path.IsAbs(n) || strings.HasPrefix(n, "../") || n == ".." {
		return "", false
	}
	clean := path.Clean(n)
	if clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}
