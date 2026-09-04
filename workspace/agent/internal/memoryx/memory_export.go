package memoryx

// Export side of agent-memory version management (docs/log/39 ⑤ / ADR 0022 decision 5).
//
// The default is a git bundle (all history, all refs): one file carries the history along
// with it and the receiving side can verify integrity with `git bundle verify`. A tar.gz
// (HEAD tree only) sits alongside it for taking just the latest state out lightly.
//
// Everything goes through a secret scan before it is generated (docs/log/39 ★4,
// resolution #2). A detection blocks by default and only passes once the owner has looked at
// the contents and re-issued the request with ack=1. It stops at the API alone rather than
// relying on the UI's confirmation because export is the only route that takes personal data
// out of the environment.

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

const (
	memoryFormatBundle = "bundle"
	memoryFormatTar    = "tar"
)

// memoryWorkDir is where export temporaries and import uploads live. It sits on the same
// mount as the repo, to avoid cross-device moves over EFS and not to depend on $TMPDIR's
// lifetime.
func memoryWorkDir() string { return filepath.Join(claude.ConfigDir(), "af-memory.work") }

// memoryExportManifest is the self-description placed at the head of the tar.gz. Import works
// without it, but it lets a person see which environment the archive came from and when.
type memoryExportManifest struct {
	Format      string   `json:"format"` // "af-memory-tar"
	Version     int      `json:"version"`
	GeneratedAt string   `json:"generatedAt"`
	Head        string   `json:"head"`
	Kinds       []string `json:"kinds"`
	Files       int      `json:"files"`
}

// memoryExportName is the download filename, chosen so the recipient can tell what is inside.
func memoryExportName(format string, now time.Time) string {
	ts := now.UTC().Format("20060102T150405Z")
	if format == memoryFormatTar {
		return "af-memory-" + ts + ".tar.gz"
	}
	return "af-memory-" + ts + ".bundle"
}

// memoryExportScan scans what is about to be exported. A bundle carries all history, so
// every reachable blob; a tar only the HEAD tree. The scanned range has to match the
// exported one.
func memoryExportScan(format string) ([]memorySecretFinding, error) {
	if format == memoryFormatTar {
		return memoryScanRevTree(memoryBranch)
	}
	return memoryScanAllReachable()
}

// memoryExportBundle packs all history and all refs into one file with
// `git bundle create --all`. The output is a temporary under memoryWorkDir that the caller
// removes once it has been served.
func memoryExportBundle() (string, error) {
	if err := os.MkdirAll(memoryWorkDir(), 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(memoryWorkDir(), "export-*.bundle")
	if err != nil {
		return "", err
	}
	path := f.Name()
	_ = f.Close()
	// git creates the output itself; remove the empty placeholder first so it does not trip
	// over the file already existing.
	_ = os.Remove(path)
	if _, err := memoryGitRun("bundle", "create", path, "--all"); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("git bundle create: %w", err)
	}
	return path, nil
}

// memoryExportTar tars up the HEAD tree alone: latest state only, no history.
func memoryExportTar(now time.Time) (string, error) {
	if err := os.MkdirAll(memoryWorkDir(), 0o700); err != nil {
		return "", err
	}
	head, err := memoryGitRun("rev-parse", memoryBranch)
	if err != nil {
		return "", err
	}
	listed, err := memoryGitRun("ls-tree", "-r", "--long", memoryBranch)
	if err != nil {
		return "", err
	}
	type entry struct {
		sha  string
		size int64
		path string
	}
	var entries []entry
	kinds, seenKind := []string{}, map[string]bool{}
	for _, line := range strings.Split(listed, "\n") {
		meta, p, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) < 4 || f[1] != "blob" {
			continue
		}
		size, perr := strconv.ParseInt(f[3], 10, 64)
		if perr != nil {
			continue
		}
		entries = append(entries, entry{sha: f[2], size: size, path: p})
		if k, _, ok := strings.Cut(p, "/"); ok && !seenKind[k] {
			seenKind[k] = true
			kinds = append(kinds, k)
		}
	}

	out, err := os.CreateTemp(memoryWorkDir(), "export-*.tar.gz")
	if err != nil {
		return "", err
	}
	path := out.Name()
	fail := func(e error) (string, error) {
		_ = out.Close()
		_ = os.Remove(path)
		return "", e
	}
	gw := gzip.NewWriter(out)
	tw := tar.NewWriter(gw)
	manifest, _ := json.MarshalIndent(memoryExportManifest{
		Format: "af-memory-tar", Version: 1, GeneratedAt: now.UTC().Format(time.RFC3339),
		Head: head, Kinds: kinds, Files: len(entries),
	}, "", "  ")
	if err := memoryTarAdd(tw, "manifest.json", manifest, now); err != nil {
		return fail(err)
	}
	for _, e := range entries {
		blob, berr := memoryGit("cat-file", "blob", e.sha).Output()
		if berr != nil {
			return fail(fmt.Errorf("read %s: %w", e.path, berr))
		}
		if err := memoryTarAdd(tw, e.path, blob, now); err != nil {
			return fail(err)
		}
	}
	if err := tw.Close(); err != nil {
		return fail(err)
	}
	if err := gw.Close(); err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// memoryTarAdd writes one entry, in the same style as cleanup_archive.go's tarAdd.
func memoryTarAdd(tw *tar.Writer, name string, b []byte, now time.Time) error {
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o600, Size: int64(len(b)), ModTime: now, Typeflag: tar.TypeReg,
	}); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

// HandleMemoryExport generates a bundle / tar.gz and serves it for download (?format=&ack=).
//
// On a secret detection it answers 409 with the findings; the Console shows them and then
// re-issues the request with ack=1. The findings never carry the raw secret
// (memory_secrets.go).
func HandleMemoryExport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := strings.TrimSpace(q.Get("format"))
	if format == "" {
		format = memoryFormatBundle
	}
	if format != memoryFormatBundle && format != memoryFormatTar {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRequest, "format must be \"bundle\" or \"tar\"")
		return
	}
	if err := memoryEnsureRepo(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemoryExportFailed, err.Error())
		return
	}
	if !memoryHasCommits() {
		httpx.WriteErr(w, http.StatusNotFound, errCodeMemoryNoSnapshots, "no snapshots yet")
		return
	}

	findings, err := memoryExportScan(format)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemoryExportFailed, err.Error())
		return
	}
	ack := q.Get("ack") == "1" || q.Get("ack") == "true"
	if len(findings) > 0 && !ack {
		httpx.WriteJSON(w, http.StatusConflict, map[string]any{
			"error": map[string]string{
				"code":    errCodeMemorySecretDetected,
				"message": fmt.Sprintf("%d possible secret(s) found in the memory being exported", len(findings)),
			},
			"secrets": findings,
		})
		return
	}

	now := time.Now()
	var path string
	if format == memoryFormatTar {
		path, err = memoryExportTar(now)
	} else {
		path, err = memoryExportBundle()
	}
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemoryExportFailed, err.Error())
		return
	}
	defer os.Remove(path) // always drop the temporary: no plaintext export may be left behind

	f, err := os.Open(path)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemoryExportFailed, err.Error())
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemoryExportFailed, err.Error())
		return
	}
	name := memoryExportName(format, now)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	// Record in the response header that the export went ahead over a detection, as
	// corroborating evidence for an audit.
	if len(findings) > 0 {
		w.Header().Set("X-AF-Memory-Secrets", strconv.Itoa(len(findings)))
	}
	http.ServeContent(w, r, name, st.ModTime(), f)
}
