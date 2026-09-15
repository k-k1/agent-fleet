package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/filemeta"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Read-only file browser for the Console explorer (docs/17 P3-5 stage 2). Rooted at
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
// docs/log/34, where the CP stages docs under the workspace dataDir instead). NOTE:
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

// codexGeneratedImagesRoot is the sole readable exception beneath Codex's
// otherwise-private state directory. image_gen writes its finished images here;
// auth, configuration, sessions, and every other CODEX_HOME child remain denied.
func codexGeneratedImagesRoot() string {
	return filepath.Join(paths.CodexHome(), "generated_images")
}

// isCodexGeneratedImagesPath reports whether p is the generated-images root or
// one of its descendants. It prevents the File tree and search APIs from
// enumerating this private-state exception: generated files are reachable only
// through the concrete userfile path attached to a Codex transcript.
func isCodexGeneratedImagesPath(p string) bool {
	root, err := filepath.Abs(codexGeneratedImagesRoot())
	if err != nil {
		return false
	}
	full, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, full)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveAbs handles an absolute query path (see safeBrowsePath). It serves the file only
// when it sits under an allowed read root, returning the absolute path plus a display path
// (home-relative when under the browse root, else the absolute path). Denylisted paths
// under the browse root are refused.
func resolveAbs(clean, root string) (full, rel string, ok bool) {
	// CODEX_HOME is normally beneath the browse root, where .codex is denied.
	// Check the one generated-image subdirectory first so this narrow read-only
	// exception is not swallowed by that broader denylist.
	if isCodexGeneratedImagesPath(clean) {
		return clean, clean, true
	}
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
	".copilot":              true, // copilot auth token (plaintext without a keychain) + session store
	".cursor":               true, // cursor chats/store.db + transcripts + hooks/cli config
	".config/cursor":        true, // cursor auth.json (accessToken/refreshToken in plaintext)
	".kiro":                 true, // kiro settings + v2 session store (sessions/cli)
	".local/share/kiro-cli": true, // kiro auth (data.sqlite3 auth_kv, effectively plaintext) + classic store
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

// fsResolvedOK re-validates full AFTER symlink resolution against the browse
// root + denylist. The lexical checks in safeBrowsePath cannot see a symlink
// like ~/repos/x → ~/.ssh; the file-READ path is defended separately (openat2
// RESOLVE_BENEATH|NO_SYMLINKS), but listing and the mutating handlers operate on
// the plain path and need this re-check. A not-yet-existing suffix (mkdir /
// upload / rename targets) is fine as long as every EXISTING component resolves
// and the resolved base stays in bounds.
func fsResolvedOK(full string) bool { return fsResolvedOKUnder(full, browseRoot(), true) }

// fsQueryResolvedOK is the tree/search variant: a RELATIVE query re-checks
// against the browse root (+denylist); an ABSOLUTE query (a scratch/docs read
// root, or an absolute path under home) re-checks against whichever allowed
// read root admitted it — otherwise a symlink under home could still expose
// denylisted / out-of-root METADATA (listings, name search) even though the
// content read itself is openat2-guarded.
func fsQueryResolvedOK(q, full string) bool {
	if !filepath.IsAbs(strings.TrimSpace(q)) {
		return fsResolvedOK(full)
	}
	if r, err := filepath.Rel(browseRoot(), full); err == nil &&
		r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return fsResolvedOKUnder(full, browseRoot(), true)
	}
	for _, ar := range []string{scratchRoot(), agentFleetDocsRoot()} {
		if r, err := filepath.Rel(ar, full); err == nil &&
			r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return fsResolvedOKUnder(full, ar, false) // read-only roots carry no denylist
		}
	}
	return false
}

func fsResolvedOKUnder(full, root string, applyDeny bool) bool {
	rroot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	p := filepath.Clean(full)
	suffix := ""
	for {
		r, err := filepath.EvalSymlinks(p)
		if err == nil {
			resolved := filepath.Join(r, suffix)
			rel, rerr := filepath.Rel(rroot, resolved)
			if rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return false
			}
			return rel == "." || !applyDeny || !isDenied(filepath.ToSlash(rel))
		}
		if !os.IsNotExist(err) {
			return false
		}
		if _, lerr := os.Lstat(p); lerr == nil {
			return false // exists but unresolvable: a dangling or looping symlink
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		suffix = filepath.Join(filepath.Base(p), suffix)
		p = parent
	}
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
	// Mtime is the entry's modification time in unix SECONDS, and rides on directories as
	// well as files: "newest first" and "3 minutes ago" cannot be written without it
	// (ADR 0080 decision 2). Generated images happen to sort by name because their filename
	// carries a unixnano stamp, but that is true of generated images alone — a folder of
	// screenshots has no such luck.
	//
	// An older Agent does not send it (the Agent ships separately from the Console: native
	// installs, pinned versions). A reader that gets none falls back to name order and shows
	// no relative time; it must not compare versions to decide, the same rule as thumb's
	// advisory argument in fs_thumb.go.
	Mtime int64 `json:"mtime,omitempty"`
	// Preview and Images describe what is INSIDE a directory entry, and are filled only when
	// the caller asked with `peek=<n>` (ADR 0080 P2). They exist so a folder card can show a
	// cover picture and a count: the alternative is one `fs/tree` per card, which is what
	// decision 9 refused. Advisory like `thumb` — an older Agent sends neither and the reader
	// draws the plain folder icon it always drew.
	Preview []fsPreview `json:"preview,omitempty"`
	Images  int         `json:"images,omitempty"`
}

// fsPreview names one picture inside a directory. The mtime rides along because the reader
// builds a thumbnail URL from it: without a version the answer is `max-age=60` instead of
// `immutable`, and every folder card would re-ask on the way back (fs_thumb.go).
type fsPreview struct {
	Name  string `json:"name"`
	Mtime int64  `json:"mtime,omitempty"`
}

const (
	// The most preview names one folder can carry. A cover is one picture; the cap is what
	// keeps a future mosaic from needing a wire change, and what bounds this per listing.
	peekMaxNames = 4
	// How many directories in one listing are looked into at all, and for how long. A listing
	// must stay a listing: peeking is one ReadDir plus a stat per file INSIDE each folder, and
	// a browse root full of repositories would otherwise turn one request into thousands of
	// stats. Past either bound the remaining folders simply carry no preview — advisory, so
	// there is no error to render.
	peekMaxDirs = 60
	peekBudget  = 300 * time.Millisecond
	// Directories remembered. Each is a handful of names; a miss costs one ReadDir.
	peekMemoMax = 512
)

// peekNames reads the `peek` query value: how many preview names the caller wants, or 0 for
// none. Out of range is "none" rather than an error — same rule as thumbEdge.
func peekNames(raw string) int {
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > peekMaxNames {
		return 0
	}
	return n
}

type peekResult struct {
	preview []fsPreview
	images  int
}

// Memoized by the directory's own mtime, which changes whenever an entry is added or removed —
// exactly when a cover or a count can change. The gallery re-lists every 20 seconds while a
// session runs, and re-walking every subfolder that often would spend a shared host's I/O to
// discover the same answer.
//
// Deliberately NOT sessionx's countGeneratedImages, which memoizes the same way: that one
// answers the SESSION wire, counts only, and lives in another package. Reaching across for it
// would tie the file listing to the session list; the shared part is one ReadDir loop.
var peekMemo = struct {
	mu sync.Mutex
	m  map[string]peekEntry
}{m: map[string]peekEntry{}}

type peekEntry struct {
	mod time.Time
	res peekResult
}

// peekDir answers "what is in this folder" for a folder card: how many pictures, and the
// newest few by name. Always computes peekMaxNames of them so the memo does not depend on
// what any one caller asked for.
//
// `rel` is `full`'s browse-relative form and is what the denylist is checked against; the memo
// is keyed on `full` alone because one directory has exactly one of them.
func peekDir(full, rel string, mod time.Time) peekResult {
	peekMemo.mu.Lock()
	hit, ok := peekMemo.m[full]
	peekMemo.mu.Unlock()
	if ok && hit.mod.Equal(mod) {
		return hit.res
	}

	// Read outside the lock: one slow folder must not hold up the rest of the listing.
	ents, err := os.ReadDir(full)
	if err != nil {
		return peekResult{}
	}
	var res peekResult
	for _, e := range ents {
		// filemeta is the single axis for "is this an image" (decision 3), and it also leaves
		// out imagegen's half-written ".image-*" temporaries, which carry no extension.
		if e.IsDir() || filemeta.ImageContentType(e.Name()) == "" {
			continue
		}
		if isDenied(filepath.Join(rel, e.Name())) {
			continue
		}
		res.images++
		fi, err := e.Info()
		if err != nil {
			continue
		}
		res.preview = insertNewest(res.preview, fsPreview{Name: e.Name(), Mtime: fi.ModTime().Unix()})
	}

	peekMemo.mu.Lock()
	if len(peekMemo.m) > peekMemoMax {
		peekMemo.m = map[string]peekEntry{} // a rebuild is one ReadDir per folder still on screen
	}
	peekMemo.m[full] = peekEntry{mod: mod, res: res}
	peekMemo.mu.Unlock()
	return res
}

// insertNewest keeps the newest peekMaxNames entries, newest first, with the name as a
// tiebreak so two pictures written in the same second cannot swap places between two listings.
func insertNewest(list []fsPreview, p fsPreview) []fsPreview {
	at := len(list)
	for i, e := range list {
		if p.Mtime > e.Mtime || (p.Mtime == e.Mtime && p.Name < e.Name) {
			at = i
			break
		}
	}
	if at >= peekMaxNames {
		return list
	}
	if len(list) < peekMaxNames {
		list = append(list, fsPreview{})
	}
	copy(list[at+1:], list[at:])
	list[at] = p
	return list
}

func handleFSTree(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("path")
	full, rel, ok := safeBrowsePath(q)
	if !ok || isCodexGeneratedImagesPath(full) || !fsQueryResolvedOK(q, full) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	ents, err := os.ReadDir(full)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "not_dir", "cannot list: "+rel)
		return
	}
	// `peek=<n>` is the gallery asking what is inside each SUBFOLDER, so a folder card can
	// show a cover and a count. Bounded hard (peekMaxDirs / peekBudget): a listing must stay
	// a listing.
	peek := peekNames(r.URL.Query().Get("peek"))
	peeked, peekDeadline := 0, time.Now().Add(peekBudget)
	var previewPaths []string

	out := []fsEntry{}
	for _, e := range ents {
		childRel := filepath.Join(rel, e.Name())
		if isDenied(childRel) {
			continue
		}
		fe := fsEntry{Name: e.Name(), Type: "file"}
		if e.IsDir() {
			fe.Type = "dir"
		}
		// One Info() for both fields, and for directories too: it is a single lstat that
		// ReadDir has usually already paid for, and Size and Mtime come out of the same
		// call. (Size stays meaningless for a directory, as it always was.)
		fi, err := e.Info()
		if err == nil {
			if !e.IsDir() {
				fe.Size = fi.Size()
			}
			fe.Mtime = fi.ModTime().Unix()
		}
		if peek > 0 && e.IsDir() && err == nil && peeked < peekMaxDirs && time.Now().Before(peekDeadline) {
			peeked++
			childFull := filepath.Join(full, e.Name())
			res := peekDir(childFull, childRel, fi.ModTime())
			fe.Images = res.images
			fe.Preview = res.preview
			if len(fe.Preview) > peek {
				fe.Preview = fe.Preview[:peek]
			}
			for _, p := range fe.Preview {
				previewPaths = append(previewPaths, filepath.Join(childFull, p.Name))
			}
		}
		out = append(out, fe)
	}
	// `warm=<edge>` is the gallery saying "I am about to ask for a thumbnail of every image
	// in here". Filling the cache in the background turns those requests from ~95 ms decodes
	// into ~44 µs cache reads (measured; fs_thumb.go). The listing does not wait for it, and
	// the file tree — which lists code folders constantly — never sends the parameter.
	if edge := thumbEdge(r.URL.Query().Get("warm")); edge > 0 {
		go warmThumbDir(full, edge)
		// The covers are in OTHER folders, so warming this one does not reach them. Without
		// this a folder page (the generated root is exactly that) warms nothing at all, and
		// every cover is a cold ~50 ms decode.
		if len(previewPaths) > 0 {
			go warmThumbList(previewPaths, edge)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type == "dir" // dirs first
		}
		return out[i].Name < out[j].Name
	})
	// root: the absolute browse root, so the Console can build an absolute path for a
	// row ("Copy path"). It's the same for every entry, so it rides on the response.
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"path": rel, "entries": out, "root": browseRoot()})
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
	if ok {
		ok = fsResolvedOK(dirFull)
	}
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
	if !ok || rel == "" || !fsResolvedOK(full) {
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
	if !ok || rel == "" || !fsResolvedOK(full) {
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
	if !ok1 || !ok2 || srcRel == "" || dstRel == "" || !fsResolvedOK(srcFull) || !fsResolvedOK(dstFull) {
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
	if !ok || rel == "" || !fsResolvedOK(full) {
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
