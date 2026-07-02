package main

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	".claude":               true, // plaintext claude state (also relocated via CLAUDE_CONFIG_DIR)
	".claude.json":          true, // claude keeps this in home even with CLAUDE_CONFIG_DIR
	".config/agent-fleet":   true, // encrypted secrets store + connection state
	".ssh":                  true,
	".git-credentials":      true,
	".local/share/opencode": true, // opencode auth.json (API keys) + session db
	".codex":                true, // codex auth.json (tokens) + sessions + helper bins
	".aws":                  true, // SSM login: SSO token cache + generated configs
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

// handleFSDownload streams a file's raw bytes as an attachment. Same guards as
// the viewer (safeBrowsePath: traversal + denylist), but no size cap and no
// text/binary handling — http.ServeContent streams (Range-capable) so large or
// binary files download directly without buffering.
func handleFSDownload(w http.ResponseWriter, r *http.Request) {
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
	f, err := os.Open(full)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read_failed", err.Error())
		return
	}
	defer f.Close()
	name := filepath.Base(rel)
	w.Header().Set("Content-Type", "application/octet-stream")
	// filename* (RFC 5987) carries UTF-8 names safely.
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

const defaultMaxUpload = 64 << 20 // 64 MiB per file unless AF_UPLOAD_MAX overrides

func maxUploadBytes() int64 {
	if v := os.Getenv("AF_UPLOAD_MAX"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxUpload
}

// handleFSUpload writes uploaded files into an existing directory (multipart,
// field "file", one or more). Guards: the target dir is inside the browse root
// and not denied; each destination name is reduced to its base and re-checked
// against denylist/traversal; a per-file size cap applies. A name collision
// returns 409 with the conflicting names unless ?overwrite=1. Writes go via a
// temp file + rename so a failed upload never leaves a partial file.
func handleFSUpload(w http.ResponseWriter, r *http.Request) {
	dirFull, dirRel, ok := safeBrowsePath(r.URL.Query().Get("path"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	if fi, err := os.Stat(dirFull); err != nil || !fi.IsDir() {
		writeErr(w, http.StatusBadRequest, "not_dir", "target is not a directory")
		return
	}
	overwrite := r.URL.Query().Get("overwrite") == "1"
	max := maxUploadBytes()
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_form", "expected multipart/form-data")
		return
	}
	written := []string{}
	conflicts := []string{}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_part", err.Error())
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			continue
		}
		name := filepath.Base(part.FileName())
		if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') || strings.ContainsRune(name, filepath.Separator) {
			writeErr(w, http.StatusBadRequest, "bad_name", "invalid filename")
			return
		}
		if isDenied(filepath.Join(dirRel, name)) {
			writeErr(w, http.StatusForbidden, "denied", "destination not allowed")
			return
		}
		destFull := filepath.Join(dirFull, name)
		if _, err := os.Stat(destFull); err == nil && !overwrite {
			conflicts = append(conflicts, name)
			continue
		}
		tmp, err := os.CreateTemp(dirFull, ".upload-*")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
		n, err := io.Copy(tmp, io.LimitReader(part, max+1))
		_ = tmp.Close()
		if err != nil || n > max {
			_ = os.Remove(tmp.Name())
			if n > max {
				writeErr(w, http.StatusRequestEntityTooLarge, "too_large", "file exceeds AF_UPLOAD_MAX")
			} else {
				writeErr(w, http.StatusInternalServerError, "write_failed", "upload failed")
			}
			return
		}
		if err := os.Rename(tmp.Name(), destFull); err != nil {
			_ = os.Remove(tmp.Name())
			writeErr(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
		written = append(written, name)
	}
	if len(conflicts) > 0 && !overwrite {
		writeJSON(w, http.StatusConflict, map[string]any{"path": dirRel, "written": written, "conflicts": conflicts})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": dirRel, "written": written, "conflicts": conflicts})
}

// handleFSMkdir creates a new directory at path. The parent must already exist
// (os.Mkdir, not MkdirAll — no accidental deep create). 409 if it exists.
func handleFSMkdir(w http.ResponseWriter, r *http.Request) {
	full, rel, ok := safeBrowsePath(r.URL.Query().Get("path"))
	if !ok || rel == "" {
		writeErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	if _, err := os.Stat(full); err == nil {
		writeErr(w, http.StatusConflict, "exists", "already exists: "+rel)
		return
	}
	if err := os.Mkdir(full, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "mkdir_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": rel})
}

// handleFSNewFile creates an empty file at path (O_EXCL => 409 if it exists).
func handleFSNewFile(w http.ResponseWriter, r *http.Request) {
	full, rel, ok := safeBrowsePath(r.URL.Query().Get("path"))
	if !ok || rel == "" {
		writeErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	f, err := os.OpenFile(full, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			writeErr(w, http.StatusConflict, "exists", "already exists: "+rel)
			return
		}
		writeErr(w, http.StatusInternalServerError, "create_failed", err.Error())
		return
	}
	_ = f.Close()
	writeJSON(w, http.StatusOK, map[string]any{"path": rel})
}

// handleFSRename moves from -> to within the browse root. Both ends are guarded
// (traversal + denylist); the destination must not already exist.
func handleFSRename(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	srcFull, srcRel, ok1 := safeBrowsePath(q.Get("from"))
	dstFull, dstRel, ok2 := safeBrowsePath(q.Get("to"))
	if !ok1 || !ok2 || srcRel == "" || dstRel == "" {
		writeErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	if _, err := os.Stat(srcFull); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "no such path: "+srcRel)
		return
	}
	if _, err := os.Stat(dstFull); err == nil {
		writeErr(w, http.StatusConflict, "exists", "already exists: "+dstRel)
		return
	}
	if err := os.Rename(srcFull, dstFull); err != nil {
		writeErr(w, http.StatusInternalServerError, "rename_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": dstRel})
}

// handleFSDelete removes a file or directory (recursive). Refuses the browse
// root and denylisted paths (safeBrowsePath). The Console confirms first.
func handleFSDelete(w http.ResponseWriter, r *http.Request) {
	full, rel, ok := safeBrowsePath(r.URL.Query().Get("path"))
	if !ok || rel == "" {
		writeErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	if _, err := os.Stat(full); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "no such path: "+rel)
		return
	}
	if err := os.RemoveAll(full); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": rel})
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
