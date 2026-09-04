package main

import (
	"bytes"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Read-only tree/blob/commit browsing for internal repos (docs/reference/
// internal-git-provider, P3). The CP owns the bare repos, so it serves a repo's
// contents directly (git ls-tree / cat-file / log) without anyone cloning it —
// CP-native like the repo list, tenant-scoped via withMembership. Read only: any
// active member; no write surface.

const maxBlobPreview = 1 << 20 // 1 MiB — larger blobs return metadata only

// refRE constrains a ref/commit-ish to safe characters and forbids a leading "-"
// (so it can't be read as a git flag) and ".." (no range expressions).
var refRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)

func validGitRef(ref string) bool {
	return refRE.MatchString(ref) && !strings.Contains(ref, "..")
}

// validTreePath allows a repo-relative path (empty = root). It rejects traversal,
// absolute paths, and control bytes; git resolves `<ref>:<path>` within the repo
// anyway, this is defense in depth.
func validTreePath(p string) bool {
	if p == "" {
		return true
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, "\x00") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return false
		}
	}
	for _, r := range p {
		if r < 0x20 {
			return false
		}
	}
	return true
}

// browseCtx resolves the repo and the effective ref for a browse request within
// the caller's already-resolved membership (withMembership). It writes the error
// response and returns ok=false on any failure.
func (a gitServerAPI) browseCtx(w http.ResponseWriter, r *http.Request, mv store.MembershipView) (bareDir, ref, path string, ok bool) {
	name := r.PathValue("name")
	if !validRepoName(name) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_name", "invalid repo name"})
		return "", "", "", false
	}
	g, exists, err := a.store.GetGitRepo(r.Context(), mv.TenantID, name)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return "", "", "", false
	}
	if !exists {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "no such repo"})
		return "", "", "", false
	}
	ref = strings.TrimSpace(r.URL.Query().Get("ref"))
	if ref == "" {
		ref = g.DefaultBranch
	}
	if !validGitRef(ref) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_ref", "invalid ref"})
		return "", "", "", false
	}
	path = strings.Trim(r.URL.Query().Get("path"), "/")
	if !validTreePath(path) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_path", "invalid path"})
		return "", "", "", false
	}
	bareDir = filepath.Join(a.dataRoot, "git", mv.TenantSlug, name+".git")
	return bareDir, ref, path, true
}

// tree (GET .../repos/{name}/tree?ref=&path=) lists the entries of a directory
// at a ref. An empty/absent ref (unborn repo) yields an empty listing.
func (a gitServerAPI) tree(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	bareDir, ref, path, ok := a.browseCtx(w, r, mv)
	if !ok {
		return
	}
	treeish := ref + ":" + path // "<ref>:" is the root tree
	cmd := exec.CommandContext(r.Context(), "git", "--git-dir", bareDir, "ls-tree", "--long", "-z", treeish)
	out, err := cmd.Output()
	if err != nil {
		// Unborn branch or path-not-a-tree → empty listing (Console shows "空").
		writeJSON(w, http.StatusOK, map[string]any{"ref": ref, "path": path, "entries": []any{}})
		return
	}
	entries := make([]map[string]any, 0)
	for _, rec := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if rec == "" {
			continue
		}
		meta, name, found := strings.Cut(rec, "\t")
		if !found {
			continue
		}
		f := strings.Fields(meta) // mode type oid size
		if len(f) < 4 {
			continue
		}
		var size int64
		if f[3] != "-" {
			size, _ = strconv.ParseInt(f[3], 10, 64)
		}
		entries = append(entries, map[string]any{
			"name": name, "type": f[1], "mode": f[0], "oid": f[2], "size": size,
		})
	}
	// Directories first, then files, each alphabetical — a natural browse order.
	sortTreeEntries(entries)
	writeJSON(w, http.StatusOK, map[string]any{"ref": ref, "path": path, "entries": entries})
}

func sortTreeEntries(entries []map[string]any) {
	less := func(i, j int) bool {
		ti, tj := entries[i]["type"] == "tree", entries[j]["type"] == "tree"
		if ti != tj {
			return ti // trees first
		}
		return entries[i]["name"].(string) < entries[j]["name"].(string)
	}
	// simple insertion sort (entry counts per dir are small)
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && less(j, j-1); j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
}

// blob (GET .../repos/{name}/blob?ref=&path=) returns a file's content. Text
// under the size cap is returned inline; larger, binary, or LFS pointer blobs
// return metadata with a flag instead of the bytes.
func (a gitServerAPI) blob(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	bareDir, ref, path, ok := a.browseCtx(w, r, mv)
	if !ok {
		return
	}
	if path == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_path", "path is required"})
		return
	}
	treeish := ref + ":" + path
	// Type must be a blob (not a tree/submodule).
	typeOut, err := exec.CommandContext(r.Context(), "git", "--git-dir", bareDir, "cat-file", "-t", treeish).Output()
	if err != nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "no such file at ref"})
		return
	}
	if strings.TrimSpace(string(typeOut)) != "blob" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "not_blob", "path is not a file"})
		return
	}
	sizeOut, _ := exec.CommandContext(r.Context(), "git", "--git-dir", bareDir, "cat-file", "-s", treeish).Output()
	size, _ := strconv.ParseInt(strings.TrimSpace(string(sizeOut)), 10, 64)

	resp := gitBlobWire{Ref: ref, Path: path, Size: size}
	if size > maxBlobPreview {
		resp.TooLarge = true
		writeJSON(w, http.StatusOK, resp)
		return
	}
	content, err := exec.CommandContext(r.Context(), "git", "--git-dir", bareDir, "cat-file", "blob", treeish).Output()
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if bytes.HasPrefix(content, []byte("version https://git-lfs")) {
		// An LFS pointer — show it as such rather than the raw pointer text.
		resp.LFS = true
		if m := lfsPointerOID.FindSubmatch(content); m != nil {
			resp.LFSOID = string(m[1])
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if bytes.IndexByte(content, 0) >= 0 {
		resp.Binary = true
		writeJSON(w, http.StatusOK, resp)
		return
	}
	text := string(content)
	resp.Content = &text
	writeJSON(w, http.StatusOK, resp)
}

// gitBlobWire — GET /api/internal-git/repos/{name}/blob のレスポンス
// （Console の `Blob`、console/src/features/settings/workspace/InternalRepoBrowser.tsx）。
//
// was: map[string]any{"ref":…, "path":…, "size":…} を resp に置き、4 つの出口が
//
//	それぞれ too_large / lfs(+lfs_oid) / binary / content を足して返す。
//	つまり**出口ごとにキー集合が違う**ので、任意キーは omitempty で表す。
//
// 🔴 Content だけポインタなのは、**omitempty が忠実にならない唯一のキー**だから:
// content は `string(content)` なので**空ファイルなら ""** になり、旧コードは
// `"content": ""` を**出す**。string + omitempty だと**その空を消してしまう**
// （在るのに消える＝ワイヤが変わる）。nil = キー無し / &"" = 空で出す、で区別する。
//
// 他の任意キーは omitempty で忠実:
//   - too_large / lfs / binary は **true しか代入されない**ので、false は「無い」と同義。
//   - lfs_oid は `([0-9a-f]{64})` の捕獲＝**必ず 64 文字**で、空を取れない
//     （git_gc.go:157 の正規表現）。
//
// ⚠️ ref は Console が自分でクエリに載せて送った値の echo。Console の `Blob` は
// これを宣言していないが**読んでもいない**ので、型化してもワイヤも画面も変わらない。
type gitBlobWire struct {
	Ref      string  `json:"ref"`
	Path     string  `json:"path"`
	Size     int64   `json:"size"`
	TooLarge bool    `json:"too_large,omitempty"`
	LFS      bool    `json:"lfs,omitempty"`
	LFSOID   string  `json:"lfs_oid,omitempty"`
	Binary   bool    `json:"binary,omitempty"`
	Content  *string `json:"content,omitempty"`
}

// commits (GET .../repos/{name}/commits?ref=&path=&limit=) returns recent
// commits touching a path (or the whole repo).
func (a gitServerAPI) commits(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	bareDir, ref, path, ok := a.browseCtx(w, r, mv)
	if !ok {
		return
	}
	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	// %x1f = field sep, records NUL-separated (-z). %aI = author date, ISO 8601.
	args := []string{"--git-dir", bareDir, "log", "-z", "--max-count=" + strconv.Itoa(limit),
		"--format=%H%x1f%s%x1f%an%x1f%aI", ref}
	if path != "" {
		args = append(args, "--", path)
	}
	out, err := exec.CommandContext(r.Context(), "git", args...).Output()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ref": ref, "path": path, "commits": []any{}})
		return
	}
	commits := make([]map[string]any, 0)
	for _, rec := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if rec == "" {
			continue
		}
		f := strings.SplitN(rec, "\x1f", 4)
		if len(f) != 4 {
			continue
		}
		commits = append(commits, map[string]any{
			"sha": f[0], "short": f[0][:min(12, len(f[0]))],
			"subject": f[1], "author": f[2], "date": f[3],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ref": ref, "path": path, "commits": commits})
}
