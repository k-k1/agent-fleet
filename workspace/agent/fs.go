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

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
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

// scratchRoot is the agent's own per-user temp/scratch base (e.g. /tmp/claude-1000),
// where the harness places each session's scratchpad. It sits OUTSIDE the browse root,
// so SendUserFile paths from there (a shared preview PNG, a generated report) would
// otherwise be un-openable. The browser is allowed to READ under it: it holds only
// agent-authored scratch — credentials live under home and are denylisted. Listing the
// tree is unaffected (still rooted at home); this only widens direct file/download reads.
func scratchRoot() string {
	return filepath.Join(os.TempDir(), "claude-"+strconv.Itoa(os.Getuid()))
}

// agentFleetDocsRoot is staged by the Control Plane per membership role and mounted
// read-only into the workspace. It is outside home, so add it explicitly to the
// read-only file-view roots; this lets the Console open the user guide without
// duplicating its Markdown in the frontend bundle. AGENT_DOCS_DIR overrides the
// fixed container path for runtimes without a mount seam (AF_RUNTIME=native,
// docs/34, where the CP stages docs under the workspace dataDir instead). NOTE:
// distinct from the CP-side AF_DOCS_DIR (the staging SOURCE, workspace_docs.go).
func agentFleetDocsRoot() string {
	if d := os.Getenv("AGENT_DOCS_DIR"); d != "" {
		return d
	}
	return "/usr/local/share/agent-fleet/docs"
}

// allowedReadRoots are the absolute roots the file browser may serve a file from when the
// query path is itself absolute (a SendUserFile path that landed outside the browse root).
// The browse root comes first so an absolute path under home maps back to a home-relative
// display path.
func allowedReadRoots() []string {
	return []string{browseRoot(), scratchRoot(), agentFleetDocsRoot()}
}

// resolveAbs handles an absolute query path (see safeBrowsePath). It serves the file only
// when it sits under an allowed read root, returning the absolute path plus a display path
// (home-relative when under the browse root, else the absolute path). Denylisted paths
// under the browse root are refused.
func resolveAbs(clean, root string) (full, rel string, ok bool) {
	for _, ar := range allowedReadRoots() {
		r, err := filepath.Rel(ar, clean)
		if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			continue // not under this root
		}
		r = filepath.ToSlash(r)
		if r == "." {
			r = ""
		}
		if ar == root {
			if isDenied(r) {
				return "", "", false
			}
			return clean, r, true
		}
		return clean, clean, true // under a non-home root (scratch or staged docs): display the absolute path
	}
	return "", "", false
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
	".gemini":               true, // agy OAuth token (plaintext) + conversation DBs
	".copilot":              true, // copilot auth token (キーチェーン無しでは平文) + session store
	".cursor":               true, // cursor chats/store.db + transcripts + hooks/cli config
	".config/cursor":        true, // cursor auth.json (accessToken/refreshToken 平文)
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

// safeBrowsePath resolves a query path to an absolute file the browser may serve, plus a
// display path. Two forms are accepted:
//   - browse-root-relative (no leading slash): the form the Console's file tree and FileView
//     use. Joined onto the browse root; traversal above the root and denylisted paths are
//     rejected.
//   - absolute (leading slash): a SendUserFile path that resolved outside the browse root
//     (e.g. a /tmp/claude-<uid> scratchpad, left absolute by toBrowseRel). Served only when
//     it sits under an allowed read root — the browse root itself, the scratch base, or the
//     role-scoped documentation mount — so a shared scratchpad or user-guide file opens in
//     the viewer instead of erroring. See resolveAbs.
func safeBrowsePath(p string) (full, rel string, ok bool) {
	root := browseRoot()
	p = strings.TrimSpace(p)
	if filepath.IsAbs(p) {
		return resolveAbs(filepath.Clean(p), root)
	}
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

// safeWritableBrowsePath deliberately keeps mutations inside the user's home.
// safeBrowsePath also admits read-only roots (scratch and the role-scoped guide),
// which must never become writable through the file API.
func safeWritableBrowsePath(p string) (full, rel string, ok bool) {
	if filepath.IsAbs(strings.TrimSpace(p)) {
		return "", "", false
	}
	return safeBrowsePath(p)
}

type fsEntry struct {
	Name string `json:"name"`
	Type string `json:"type"` // dir | file
	Size int64  `json:"size"`
}

func handleFSTree(w http.ResponseWriter, r *http.Request) {
	full, rel, ok := safeBrowsePath(r.URL.Query().Get("path"))
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	ents, err := os.ReadDir(full)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "not_dir", "cannot list: "+rel)
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
	// root: the absolute browse root, so the Console can build an absolute path for a
	// row ("パスをコピー"). It's the same for every entry, so it rides on the response.
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"path": rel, "entries": out, "root": browseRoot()})
}

func handleFSFile(w http.ResponseWriter, r *http.Request) {
	full, rel, ok := safeBrowsePath(r.URL.Query().Get("path"))
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	fi, err := os.Stat(full)
	if err != nil || fi.IsDir() {
		httpx.WriteErr(w, http.StatusNotFound, "not_file", "not a file: "+rel)
		return
	}
	if fi.Size() > maxViewBytes {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"path": rel, "size": fi.Size(), "truncated": true, "binary": false, "content": "(file too large to preview)"})
		return
	}
	b, err := os.ReadFile(full)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "read_failed", err.Error())
		return
	}
	if bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"path": rel, "size": fi.Size(), "binary": true})
		return
	}
	resp := map[string]any{"path": rel, "size": fi.Size(), "content": string(b)}
	if isLFSPointer(b) {
		// The working-tree file is a Git LFS pointer, not the real binary (LFS wasn't
		// smudged — e.g. the repo was cloned before git-lfs was configured). Flag it so
		// the viewer can say so and suggest `git lfs pull`.
		resp["lfs"] = true
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// handleFSDownload streams a file's raw bytes as an attachment. Same guards as
// the viewer (safeBrowsePath: traversal + denylist), but no size cap and no
// text/binary handling — http.ServeContent streams (Range-capable) so large or
// binary files download directly without buffering.
func handleFSDownload(w http.ResponseWriter, r *http.Request) {
	full, rel, ok := safeBrowsePath(r.URL.Query().Get("path"))
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	fi, err := os.Stat(full)
	if err != nil || fi.IsDir() {
		httpx.WriteErr(w, http.StatusNotFound, "not_file", "not a file: "+rel)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "read_failed", err.Error())
		return
	}
	defer f.Close()
	name := filepath.Base(rel)
	// Serve a proper image Content-Type for known image extensions so the Console's
	// <img> preview renders them. Raster formats (png/jpeg/…) the browser sniffs even
	// under octet-stream, but SVG is NOT sniffed in an <img> — it needs image/svg+xml,
	// or it shows as a broken image. Everything else stays octet-stream. The attachment
	// disposition below is kept regardless: <img> ignores it (so previews still render),
	// while a direct navigation to an SVG downloads it instead of executing its scripts.
	ct := "application/octet-stream"
	if it := imageContentType(name); it != "" {
		ct = it
	}
	w.Header().Set("Content-Type", ct)
	// filename* (RFC 5987) carries UTF-8 names safely.
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

// imageContentType maps a filename to its image MIME type (mirrors the Console's
// IMAGE_EXT in lib/filemeta.ts), or "" when it isn't a previewable image extension.
func imageContentType(name string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	switch ext {
	case "png":
		return "image/png"
	case "apng":
		return "image/apng"
	case "jpg", "jpeg", "jfif":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "avif":
		return "image/avif"
	case "bmp":
		return "image/bmp"
	case "ico":
		return "image/x-icon"
	case "svg":
		return "image/svg+xml"
	}
	return ""
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
	dirFull, dirRel, ok := safeWritableBrowsePath(r.URL.Query().Get("path"))
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	if fi, err := os.Stat(dirFull); err != nil || !fi.IsDir() {
		httpx.WriteErr(w, http.StatusBadRequest, "not_dir", "target is not a directory")
		return
	}
	overwrite := r.URL.Query().Get("overwrite") == "1"
	max := maxUploadBytes()
	mr, err := r.MultipartReader()
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_form", "expected multipart/form-data")
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
			httpx.WriteErr(w, http.StatusBadRequest, "bad_part", err.Error())
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			continue
		}
		name := filepath.Base(part.FileName())
		if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') || strings.ContainsRune(name, filepath.Separator) {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid filename")
			return
		}
		if isDenied(filepath.Join(dirRel, name)) {
			httpx.WriteErr(w, http.StatusForbidden, "denied", "destination not allowed")
			return
		}
		destFull := filepath.Join(dirFull, name)
		if _, err := os.Stat(destFull); err == nil && !overwrite {
			conflicts = append(conflicts, name)
			continue
		}
		tmp, err := os.CreateTemp(dirFull, ".upload-*")
		if err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
		n, err := io.Copy(tmp, io.LimitReader(part, max+1))
		_ = tmp.Close()
		if err != nil || n > max {
			_ = os.Remove(tmp.Name())
			if n > max {
				httpx.WriteErr(w, http.StatusRequestEntityTooLarge, "too_large", "file exceeds AF_UPLOAD_MAX")
			} else {
				httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", "upload failed")
			}
			return
		}
		if err := os.Rename(tmp.Name(), destFull); err != nil {
			_ = os.Remove(tmp.Name())
			httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
		written = append(written, name)
	}
	if len(conflicts) > 0 && !overwrite {
		httpx.WriteJSON(w, http.StatusConflict, map[string]any{"path": dirRel, "written": written, "conflicts": conflicts})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"path": dirRel, "written": written, "conflicts": conflicts})
}

// handleFSMkdir creates a new directory at path. The parent must already exist
// (os.Mkdir, not MkdirAll — no accidental deep create). 409 if it exists.
func handleFSMkdir(w http.ResponseWriter, r *http.Request) {
	full, rel, ok := safeWritableBrowsePath(r.URL.Query().Get("path"))
	if !ok || rel == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	if _, err := os.Stat(full); err == nil {
		httpx.WriteErr(w, http.StatusConflict, "exists", "already exists: "+rel)
		return
	}
	if err := os.Mkdir(full, 0o755); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "mkdir_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"path": rel})
}

// handleFSNewFile creates an empty file at path (O_EXCL => 409 if it exists).
func handleFSNewFile(w http.ResponseWriter, r *http.Request) {
	full, rel, ok := safeWritableBrowsePath(r.URL.Query().Get("path"))
	if !ok || rel == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	f, err := os.OpenFile(full, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			httpx.WriteErr(w, http.StatusConflict, "exists", "already exists: "+rel)
			return
		}
		httpx.WriteErr(w, http.StatusInternalServerError, "create_failed", err.Error())
		return
	}
	_ = f.Close()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"path": rel})
}

// handleFSRename moves from -> to within the browse root. Both ends are guarded
// (traversal + denylist); the destination must not already exist.
func handleFSRename(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	srcFull, srcRel, ok1 := safeWritableBrowsePath(q.Get("from"))
	dstFull, dstRel, ok2 := safeWritableBrowsePath(q.Get("to"))
	if !ok1 || !ok2 || srcRel == "" || dstRel == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	if _, err := os.Stat(srcFull); err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such path: "+srcRel)
		return
	}
	if _, err := os.Stat(dstFull); err == nil {
		httpx.WriteErr(w, http.StatusConflict, "exists", "already exists: "+dstRel)
		return
	}
	if err := os.Rename(srcFull, dstFull); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "rename_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"path": dstRel})
}

// handleFSDelete removes a file or directory (recursive). Refuses the browse
// root and denylisted paths (safeBrowsePath). The Console confirms first.
func handleFSDelete(w http.ResponseWriter, r *http.Request) {
	full, rel, ok := safeWritableBrowsePath(r.URL.Query().Get("path"))
	if !ok || rel == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	if _, err := os.Stat(full); err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such path: "+rel)
		return
	}
	if err := os.RemoveAll(full); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"deleted": rel})
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
