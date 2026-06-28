package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// Read-only file browser for the Console explorer (docs/17 P3-5 段2). Rooted at
// the browse root (default = home) with a denylist so sensitive state is never
// listed or read. Plaintext Claude state lives outside the browse root via
// CLAUDE_CONFIG_DIR; the encrypted secrets store stays in home but is denylisted.

func browseRoot() string {
	if r := os.Getenv("AF_BROWSE_ROOT"); r != "" {
		return r
	}
	return homeDir()
}

// fsDeny lists browse-root-relative paths that are never exposed.
var fsDeny = map[string]bool{
	".claude":             true, // plaintext claude state (also relocated via CLAUDE_CONFIG_DIR)
	".claude.json":        true, // claude keeps this in home even with CLAUDE_CONFIG_DIR
	".config/agent-fleet": true, // encrypted secrets store + connection state
	".ssh":                true,
	".git-credentials":    true,
	".local/share/opencode": true, // opencode auth.json (API keys) + session db
	".codex":                true, // codex auth.json (tokens) + sessions + helper bins
}

func isDenied(rel string) bool {
	rel = filepath.ToSlash(rel)
	for d := range fsDeny {
		if rel == d || strings.HasPrefix(rel, d+"/") {
			return true
		}
	}
	return false
}

// safeBrowsePath resolves a query path within the browse root, rejecting
// traversal and denylisted paths. Returns absolute + root-relative paths.
func safeBrowsePath(p string) (full, rel string, ok bool) {
	root := browseRoot()
	p = strings.TrimPrefix(strings.TrimSpace(p), "/")
	clean := filepath.Clean(p)
	if clean == "." {
		clean = ""
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", "", false
	}
	full = filepath.Join(root, clean)
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", false
	}
	if rel == "." {
		rel = ""
	}
	if isDenied(rel) {
		return "", "", false
	}
	return full, rel, true
}

type fsEntry struct {
	Name string `json:"name"`
	Type string `json:"type"` // dir | file
	Size int64  `json:"size"`
}

func handleFSTree(w http.ResponseWriter, r *http.Request) {
	full, rel, ok := safeBrowsePath(r.URL.Query().Get("path"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	ents, err := os.ReadDir(full)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_dir", "cannot list: "+rel)
		return
	}
	out := []fsEntry{}
	for _, e := range ents {
		if isDenied(filepath.Join(rel, e.Name())) {
			continue
		}
		fe := fsEntry{Name: e.Name(), Type: "file"}
		if e.IsDir() {
			fe.Type = "dir"
		} else if fi, err := e.Info(); err == nil {
			fe.Size = fi.Size()
		}
		out = append(out, fe)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type == "dir" // dirs first
		}
		return out[i].Name < out[j].Name
	})
	writeJSON(w, http.StatusOK, map[string]any{"path": rel, "entries": out})
}

func handleFSFile(w http.ResponseWriter, r *http.Request) {
	full, rel, ok := safeBrowsePath(r.URL.Query().Get("path"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	fi, err := os.Stat(full)
	if err != nil || fi.IsDir() {
		writeErr(w, http.StatusNotFound, "not_file", "not a file: "+rel)
		return
	}
	if fi.Size() > maxViewBytes {
		writeJSON(w, http.StatusOK, map[string]any{"path": rel, "size": fi.Size(), "truncated": true, "binary": false, "content": "(file too large to preview)"})
		return
	}
	b, err := os.ReadFile(full)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read_failed", err.Error())
		return
	}
	if bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
		writeJSON(w, http.StatusOK, map[string]any{"path": rel, "size": fi.Size(), "binary": true})
		return
	}
	resp := map[string]any{"path": rel, "size": fi.Size(), "content": string(b)}
	if isLFSPointer(b) {
		// The working-tree file is a Git LFS pointer, not the real binary (LFS wasn't
		// smudged — e.g. the repo was cloned before git-lfs was configured). Flag it so
		// the viewer can say so and suggest `git lfs pull`.
		resp["lfs"] = true
	}
	writeJSON(w, http.StatusOK, resp)
}

// lfsPointerMagic is the first line of a Git LFS pointer file.
const lfsPointerMagic = "version https://git-lfs.github.com/spec/v1"

// isLFSPointer reports whether b is a Git LFS pointer placeholder (a small text
// file standing in for an un-fetched binary). Mirrors CodeLeaf's isLfsPointerHead
// with the same size bounds (pointers are ~120–200 bytes).
func isLFSPointer(b []byte) bool {
	if len(b) < 50 || len(b) > 1024 {
		return false
	}
	return bytes.HasPrefix(b, []byte(lfsPointerMagic))
}
