package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// git view/edit endpoints for the Console source-control panel (docs/17 P3-5).
// All operate on a working copy under ~/repos/<name>; paths are resolved within
// the repo (traversal-guarded) and outputs are size-capped. Read endpoints
// (changes/diff/log) plus light write ops (stage/unstage/discard/commit).

const maxViewBytes = 2 << 20 // 2 MiB cap for diff/file output

// relRepoPath validates a user path is inside the repo and returns it cleaned and
// repo-relative. Rejects traversal and option-looking paths.
func relRepoPath(dir, p string) (string, bool) {
	p = strings.TrimPrefix(strings.TrimSpace(p), "/")
	if p == "" {
		return "", false
	}
	clean := filepath.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "-") {
		return "", false
	}
	rel, err := filepath.Rel(dir, filepath.Join(dir, clean))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return clean, true
}

// Change is one file in `git status` (porcelain v1): index/worktree status chars.
type Change struct {
	Path      string `json:"path"`
	Index     string `json:"index"`    // staged side (X); " " = unchanged
	Worktree  string `json:"worktree"` // worktree side (Y)
	Untracked bool   `json:"untracked"`
}

func gitChanges(dir string) ([]Change, error) {
	// core.quotePath=false: emit non-ASCII (e.g. Japanese) paths verbatim as UTF-8
	// instead of C-style octal escapes ("\346\227\245…"). Without it the escaped
	// name reaches the Console FILES list and no longer matches the real path, so
	// clicking a changed file fails to open it.
	out, err := exec.Command("git", "-C", dir, "-c", "core.quotePath=false", "status", "--porcelain").Output()
	if err != nil {
		return nil, err
	}
	cs := []Change{}
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 4 {
			continue
		}
		xy, rest := line[:2], line[3:]
		if i := strings.Index(rest, " -> "); i >= 0 { // rename: show the new path
			rest = rest[i+4:]
		}
		rest = strings.Trim(rest, `"`) // still quoted for control chars/quotes/backslashes
		cs = append(cs, Change{Path: rest, Index: string(xy[0]), Worktree: string(xy[1]), Untracked: xy == "??"})
	}
	return cs, nil
}

func handleRepoChanges(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoDirFromPath(w, r)
	if !ok {
		return
	}
	cs, err := gitChanges(dir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "git_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": cs})
}

func handleRepoDiff(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoDirFromPath(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	// core.quotePath=false: keep non-ASCII paths in the +++/--- headers as UTF-8
	// so the Console renders them as text (see gitChanges).
	args := []string{"-C", dir, "-c", "core.quotePath=false", "diff"}
	if v := q.Get("staged"); v == "1" || v == "true" {
		args = append(args, "--staged")
	}
	if p := q.Get("path"); p != "" {
		rel, ok := relRepoPath(dir, p)
		if !ok {
			writeErr(w, http.StatusBadRequest, "bad_path", "invalid path")
			return
		}
		args = append(args, "--", rel)
	}
	out, err := exec.Command("git", args...).Output() // diff exits 0 even with changes
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "git_failed", err.Error())
		return
	}
	s := string(out)
	truncated := false
	if len(s) > maxViewBytes {
		s, truncated = s[:maxViewBytes], true
	}
	writeJSON(w, http.StatusOK, map[string]any{"diff": s, "truncated": truncated})
}

type commitView struct {
	Hash    string `json:"hash"`
	Short   string `json:"short"`
	Author  string `json:"author"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
}

func handleRepoLog(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoDirFromPath(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit := 50
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 500 {
		limit = n
	}
	args := []string{"-C", dir, "log", "--max-count=" + strconv.Itoa(limit),
		"--pretty=format:%H%x1f%h%x1f%an%x1f%aI%x1f%s"}
	if ref := q.Get("ref"); ref != "" {
		if strings.HasPrefix(ref, "-") {
			writeErr(w, http.StatusBadRequest, "bad_ref", "ref must not start with '-'")
			return
		}
		args = append(args, ref)
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "git_failed", err.Error())
		return
	}
	commits := []commitView{}
	for _, line := range strings.Split(string(out), "\n") {
		if p := strings.Split(line, "\x1f"); len(p) == 5 {
			commits = append(commits, commitView{Hash: p[0], Short: p[1], Author: p[2], Date: p[3], Subject: p[4]})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"commits": commits})
}

// shaRe guards the ?sha= param: hex only (full or abbreviated), so it can't be an
// option flag or a revision range that reaches outside the single commit.
var shaRe = regexp.MustCompile(`^[0-9a-fA-F]{4,64}$`)

type commitFile struct {
	Status string `json:"status"` // M / A / D / R100 / ...
	Path   string `json:"path"`
}

// handleRepoShow returns one commit's detail for the history → diff pane (codeleaf
// CommitDetail style): header (subject/body/author/date/sha), the changed-file list,
// and the full colored patch. Diff is size-capped like handleRepoDiff.
func handleRepoShow(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoDirFromPath(w, r)
	if !ok {
		return
	}
	sha := strings.TrimSpace(r.URL.Query().Get("sha"))
	if !shaRe.MatchString(sha) {
		writeErr(w, http.StatusBadRequest, "bad_sha", "sha must be a hex commit id")
		return
	}

	// Header: one record, fields separated by \x1f; body is last (may hold newlines).
	hdr, err := exec.Command("git", "-C", dir, "log", "-1",
		"--pretty=format:%H%x1f%h%x1f%an%x1f%aI%x1f%s%x1f%b", sha).Output()
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "no such commit")
		return
	}
	out := map[string]any{}
	if p := strings.SplitN(string(hdr), "\x1f", 6); len(p) == 6 {
		out["hash"], out["short"], out["author"] = p[0], p[1], p[2]
		out["date"], out["subject"], out["body"] = p[3], p[4], strings.TrimRight(p[5], "\n")
	}

	// Changed files (name-status). First token = status, last = path (rename → new).
	files := []commitFile{}
	if ns, err := exec.Command("git", "-C", dir, "-c", "core.quotePath=false", "show", "--name-status",
		"--format=", "--no-color", sha).Output(); err == nil {
		for _, line := range strings.Split(string(ns), "\n") {
			f := strings.Split(strings.TrimSpace(line), "\t")
			if len(f) >= 2 && f[0] != "" {
				files = append(files, commitFile{Status: f[0], Path: f[len(f)-1]})
			}
		}
	}
	out["files"] = files

	// Full patch.
	diff, err := exec.Command("git", "-C", dir, "-c", "core.quotePath=false", "show", "--format=", "--no-color", sha).Output()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "git_failed", err.Error())
		return
	}
	s := string(diff)
	truncated := false
	if len(s) > maxViewBytes {
		s, truncated = s[:maxViewBytes], true
	}
	out["diff"], out["truncated"] = s, truncated
	writeJSON(w, http.StatusOK, out)
}

type pathsReq struct {
	Paths []string `json:"paths"`
}

// validPaths cleans+guards every path against the repo dir.
func validPaths(dir string, ps []string) ([]string, bool) {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		rel, ok := relRepoPath(dir, p)
		if !ok {
			return nil, false
		}
		out = append(out, rel)
	}
	return out, len(out) > 0
}

func gitPathsOp(w http.ResponseWriter, r *http.Request, gitArgs []string, code string) {
	dir, ok := repoDirFromPath(w, r)
	if !ok {
		return
	}
	var req pathsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	paths, ok := validPaths(dir, req.Paths)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_path", "paths required and must be inside the repo")
		return
	}
	args := append([]string{"-C", dir}, gitArgs...)
	args = append(args, "--")
	args = append(args, paths...)
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		writeErr(w, http.StatusBadGateway, code, fmt.Sprintf("%v: %s", err, strings.TrimSpace(string(out))))
		return
	}
	cs, _ := gitChanges(dir)
	writeJSON(w, http.StatusOK, map[string]any{"changes": cs})
}

func handleRepoStage(w http.ResponseWriter, r *http.Request) {
	gitPathsOp(w, r, []string{"add"}, "stage_failed")
}

func handleRepoUnstage(w http.ResponseWriter, r *http.Request) {
	gitPathsOp(w, r, []string{"restore", "--staged"}, "unstage_failed")
}

// handleRepoDiscard is destructive (drops worktree changes); the Console confirms.
func handleRepoDiscard(w http.ResponseWriter, r *http.Request) {
	gitPathsOp(w, r, []string{"restore"}, "discard_failed")
}

type commitReq struct {
	Message string `json:"message"`
	All     bool   `json:"all"`
}

func handleRepoCommit(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoDirFromPath(w, r)
	if !ok {
		return
	}
	var req commitReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		writeErr(w, http.StatusBadRequest, "bad_message", "commit message is required")
		return
	}
	applyGitIdentity(dir) // self-heal: ensure the effective identity is in local config
	args := []string{"-C", dir, "commit"}
	if req.All {
		args = append(args, "-a")
	}
	args = append(args, "-m", req.Message)
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		writeErr(w, http.StatusBadGateway, "commit_failed", fmt.Sprintf("%v: %s", err, strings.TrimSpace(string(out))))
		return
	}
	st, _ := gitStatus(dir)
	writeJSON(w, http.StatusOK, st)
}
