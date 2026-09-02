package gitx

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// repoViewDir resolves the optional read-only SCM target below a top-level
// working copy. It lets the Console inspect an initialized submodule without
// pretending that the nested checkout is another ~/repos entry.
func repoViewDir(w http.ResponseWriter, r *http.Request) (string, bool) {
	dir, ok := repoDirFromPath(w, r)
	if !ok {
		return "", false
	}
	p := r.URL.Query().Get("path")
	if p == "" {
		return dir, true
	}
	rel, ok := relRepoPath(dir, p)
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_path", "invalid submodule path")
		return "", false
	}
	// Only a gitlink declared by the parent may be selected. Merely finding an
	// embedded .git directory is not enough: that would turn this read endpoint
	// into a generic nested-repository browser.
	stage, err := gitx.Run(dir, "ls-files", "--stage", "--", rel)
	if err != nil || !strings.HasPrefix(stage, "160000 ") {
		httpx.WriteErr(w, http.StatusBadRequest, "not_submodule", "path is not a submodule: "+rel)
		return "", false
	}
	target := filepath.Join(dir, rel)
	if !isGitRepo(target) {
		httpx.WriteErr(w, http.StatusNotFound, "submodule_unavailable", "submodule is not initialized: "+rel)
		return "", false
	}
	return target, true
}

type submoduleView struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Initialized bool   `json:"initialized"`
	SHA         string `json:"sha,omitempty"`
}

// handleRepoSubmodules detects submodules from .gitmodules. Initialized nested
// checkouts become selectable SCM graph targets; missing ones remain visible so
// the UI can explain why their history cannot be opened.
func handleRepoSubmodules(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoDirFromPath(w, r)
	if !ok {
		return
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitmodules")); err != nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"submodules": []submoduleView{}})
		return
	}
	out, err := gitx.Run(dir, "config", "--file", ".gitmodules", "--get-regexp", `^submodule\..*\.path$`)
	if err != nil && strings.TrimSpace(out) == "" {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"submodules": []submoduleView{}})
		return
	}
	items := []submoduleView{}
	for _, line := range strings.Split(out, "\n") {
		key, path, found := strings.Cut(strings.TrimSpace(line), " ")
		path = strings.TrimSpace(path)
		if !found || path == "" {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(key, "submodule."), ".path")
		rel, valid := relRepoPath(dir, path)
		if !valid {
			continue
		}
		sm := submoduleView{Name: name, Path: rel, Initialized: isGitRepo(filepath.Join(dir, rel))}
		if sm.Initialized {
			sm.SHA, _ = gitx.Run(filepath.Join(dir, rel), "rev-parse", "--short", "HEAD")
		}
		items = append(items, sm)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"submodules": items})
}

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
	// Raw .Output() (not runGit): porcelain lines may START with a space (" M foo")
	// and trimming would corrupt the first line's XY status columns.
	out, err := gitx.Cmd(dir, "-c", "core.quotePath=false", "status", "--porcelain").Output()
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
		httpx.WriteErr(w, http.StatusInternalServerError, "git_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"changes": cs})
}

func handleRepoDiff(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoDirFromPath(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	// core.quotePath=false: keep non-ASCII paths in the +++/--- headers as UTF-8
	// so the Console renders them as text (see gitChanges).
	args := []string{"-c", "core.quotePath=false", "diff"}
	if v := q.Get("staged"); v == "1" || v == "true" {
		args = append(args, "--staged")
	}
	if p := q.Get("path"); p != "" {
		rel, ok := relRepoPath(dir, p)
		if !ok {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_path", "invalid path")
			return
		}
		args = append(args, "--", rel)
	}
	// Raw .Output() (not runGit): the patch body is returned verbatim in the JSON
	// response and must stay byte-identical (no trimming).
	out, err := gitx.Cmd(dir, args...).Output() // diff exits 0 even with changes
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "git_failed", err.Error())
		return
	}
	s := string(out)
	truncated := false
	if len(s) > maxViewBytes {
		s, truncated = s[:maxViewBytes], true
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"diff": s, "truncated": truncated})
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
	args := []string{"log", "--max-count=" + strconv.Itoa(limit),
		"--pretty=format:%H%x1f%h%x1f%an%x1f%aI%x1f%s"}
	if ref := q.Get("ref"); ref != "" {
		if strings.HasPrefix(ref, "-") {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_ref", "ref must not start with '-'")
			return
		}
		args = append(args, ref)
	}
	out, err := gitx.Run(dir, args...)
	if err != nil {
		// An unborn branch (a folder from POST /repos/init, or a clone of an empty
		// remote) has no commits, and `git log` calls that fatal. "No history yet" is
		// not a failure to report — the graph view already answers it with an empty
		// list, because `git log --all` exits 0 there.
		if st, sErr := gitStatus(dir); sErr == nil && st.Unborn {
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"commits": []commitView{}})
			return
		}
		httpx.WriteErr(w, http.StatusInternalServerError, "git_failed", err.Error())
		return
	}
	commits := []commitView{}
	for _, line := range strings.Split(out, "\n") {
		if p := strings.Split(line, "\x1f"); len(p) == 5 {
			commits = append(commits, commitView{Hash: p[0], Short: p[1], Author: p[2], Date: p[3], Subject: p[4]})
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"commits": commits})
}

// graphRef is one ref badge on a commit: a display name + its kind (head/remote/tag).
type graphRef struct {
	Name string `json:"name"`
	Type string `json:"type"` // head | remote | tag
}

// graphCommit is one node in the commit-graph DAG: its sha + parents (for lane layout),
// the usual metadata, decorating refs, and whether it is reachable from HEAD (commits
// that aren't get dimmed / drawn hollow, codeleaf-style).
type graphCommit struct {
	Sha      string     `json:"sha"`
	Short    string     `json:"short"`
	Parents  []string   `json:"parents"`
	Author   string     `json:"author"`
	Date     string     `json:"date"`
	Subject  string     `json:"subject"`
	Refs     []graphRef `json:"refs"`
	InBranch bool       `json:"inBranch"`
}

// parseDecorate turns git's `%D` decoration into structured refs and returns the
// current branch name (from "HEAD -> …"), if any. It expects the FULL refname form
// (--decorate=full): "HEAD -> refs/heads/main, refs/remotes/origin/main, refs/tags/v1".
// Full names are used because the short form is ambiguous for slash-containing branch
// names (local "feat/x" vs remote "origin/feat/x"). A short-form fallback is kept so a
// stray short decoration still classifies sanely.
func parseDecorate(d string) ([]graphRef, string) {
	refs := []graphRef{}
	current := ""
	for _, tok := range strings.Split(d, ", ") {
		tok = strings.TrimSpace(tok)
		// "HEAD -> <ref>" marks the current branch; strip the arrow and classify <ref>.
		if rest, ok := strings.CutPrefix(tok, "HEAD -> "); ok {
			current = strings.TrimPrefix(strings.TrimPrefix(rest, "refs/heads/"), "heads/")
			tok = rest
		}
		// git marks tags with a "tag: " prefix in BOTH decoration forms, so
		// --decorate=full emits "tag: refs/tags/v1.0". The marker has to come off before
		// the refname is classified — otherwise the token matched neither the full-form
		// nor the short-form tag case cleanly and the chip showed the raw "refs/tags/…".
		isTag := false
		if rest, ok := strings.CutPrefix(tok, "tag: "); ok {
			isTag, tok = true, rest
		}
		switch {
		case tok == "" || tok == "HEAD" || strings.HasSuffix(tok, "/HEAD"):
			// bare HEAD (detached) or a remote's symbolic HEAD (refs/remotes/origin/HEAD) — noise
		case isTag || strings.HasPrefix(tok, "refs/tags/"):
			refs = append(refs, graphRef{Name: strings.TrimPrefix(tok, "refs/tags/"), Type: "tag"})
		case strings.HasPrefix(tok, "refs/remotes/"):
			refs = append(refs, graphRef{Name: strings.TrimPrefix(tok, "refs/remotes/"), Type: "remote"})
		case strings.HasPrefix(tok, "refs/heads/"):
			refs = append(refs, graphRef{Name: strings.TrimPrefix(tok, "refs/heads/"), Type: "head"})
		case strings.Contains(tok, "/"): // short-form fallback: assume a remote-tracking ref
			refs = append(refs, graphRef{Name: tok, Type: "remote"})
		default: // short-form fallback: a plain name is a local branch
			refs = append(refs, graphRef{Name: tok, Type: "head"})
		}
	}
	return refs, current
}

// handleRepoGraph returns the commit-graph DAG for the codeleaf-style lane view: all
// refs as roots, topo + date ordered, newest-first, each with parents + decorating
// refs, plus reachability-from-HEAD so the Console can dim off-branch commits.
func handleRepoGraph(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoViewDir(w, r)
	if !ok {
		return
	}
	limit := 300
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 1000 {
		limit = n
	}
	// %H sha, %h short, %P parents(sp), %an author, %cI committer-ISO, %s subject, %D decoration.
	// --decorate=full so %D emits full refnames (refs/heads/…, refs/remotes/…,
	// refs/tags/…). The short form is ambiguous for slash-containing branch names — a
	// local "feat/x" and a remote "origin/feat/x" both just look like "a/b" — which made
	// parseDecorate misclassify local branches as remotes (no branch-checkout offered).
	out, err := gitx.Run(dir, "log", "--all", "--topo-order", "--date-order",
		"--decorate=full", "--max-count="+strconv.Itoa(limit),
		"--pretty=format:%H%x1f%h%x1f%P%x1f%an%x1f%cI%x1f%s%x1f%D")
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "git_failed", err.Error())
		return
	}
	// Reachable-from-HEAD set → inBranch. Empty (detached / no HEAD) ⇒ dim nothing.
	reachable := map[string]bool{}
	if rl, err := gitx.Run(dir, "rev-list", "HEAD"); err == nil {
		for _, s := range strings.Fields(rl) {
			reachable[s] = true
		}
	}
	commits := []graphCommit{}
	current := ""
	for _, line := range strings.Split(out, "\n") {
		p := strings.Split(line, "\x1f")
		if len(p) != 7 {
			continue
		}
		parents := []string{}
		if s := strings.TrimSpace(p[2]); s != "" {
			parents = strings.Fields(s)
		}
		refs, cur := parseDecorate(p[6])
		if cur != "" {
			current = cur
		}
		commits = append(commits, graphCommit{
			Sha: p[0], Short: p[1], Parents: parents, Author: p[3], Date: p[4], Subject: p[5],
			Refs: refs, InBranch: len(reachable) == 0 || reachable[p[0]],
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"commits": commits, "current": current})
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
	dir, ok := repoViewDir(w, r)
	if !ok {
		return
	}
	sha := strings.TrimSpace(r.URL.Query().Get("sha"))
	if !shaRe.MatchString(sha) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_sha", "sha must be a hex commit id")
		return
	}

	// Header: one record, fields separated by \x1f; body is last (may hold newlines).
	hdr, err := gitx.Run(dir, "log", "-1",
		"--pretty=format:%H%x1f%h%x1f%an%x1f%aI%x1f%s%x1f%b", sha)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such commit")
		return
	}
	out := map[string]any{}
	if p := strings.SplitN(hdr, "\x1f", 6); len(p) == 6 {
		out["hash"], out["short"], out["author"] = p[0], p[1], p[2]
		out["date"], out["subject"], out["body"] = p[3], p[4], strings.TrimRight(p[5], "\n")
	}

	// Changed files (name-status). First token = status, last = path (rename → new).
	files := []commitFile{}
	if ns, err := gitx.Run(dir, "-c", "core.quotePath=false", "show", "--name-status",
		"--format=", "--no-color", sha); err == nil {
		for _, line := range strings.Split(ns, "\n") {
			f := strings.Split(strings.TrimSpace(line), "\t")
			if len(f) >= 2 && f[0] != "" {
				files = append(files, commitFile{Status: f[0], Path: f[len(f)-1]})
			}
		}
	}
	out["files"] = files

	// Full patch. Raw .Output() (not runGit): the patch body is returned verbatim
	// in the JSON response and must stay byte-identical (no trimming).
	diff, err := gitx.Cmd(dir, "-c", "core.quotePath=false", "show", "--format=", "--no-color", sha).Output()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "git_failed", err.Error())
		return
	}
	s := string(diff)
	truncated := false
	if len(s) > maxViewBytes {
		s, truncated = s[:maxViewBytes], true
	}
	out["diff"], out["truncated"] = s, truncated
	httpx.WriteJSON(w, http.StatusOK, out)
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
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	paths, ok := validPaths(dir, req.Paths)
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_path", "paths required and must be inside the repo")
		return
	}
	args := append([]string{}, gitArgs...)
	args = append(args, "--")
	args = append(args, paths...)
	if out, err := gitx.Combined(dir, args...); err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, code, fmt.Sprintf("%v: %s", err, out))
		return
	}
	cs, _ := gitChanges(dir)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"changes": cs})
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
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_message", "commit message is required")
		return
	}
	applyGitIdentity(dir) // self-heal: ensure the effective identity is in local config
	args := []string{"commit"}
	if req.All {
		args = append(args, "-a")
	}
	args = append(args, "-m", req.Message)
	if out, err := gitx.Combined(dir, args...); err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "commit_failed", fmt.Sprintf("%v: %s", err, out))
		return
	}
	st, _ := gitStatus(dir)
	httpx.WriteJSON(w, http.StatusOK, st)
}
