package main

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// git-aware file-browser endpoints (docs/17 P3-5, FILES 改善):
//   GET /fs/changes   — modified files across all repos (the "changes only" filter)
//   GET /fs/linemarks — editor-style gutter marks for one working-tree file
// Both reuse the read-only browse guards (denylist/traversal) and the existing
// gitChanges() porcelain parser.

type fileChange struct {
	Repo      string `json:"repo"`
	Path      string `json:"path"` // home-relative: repos/<repo>/<file>
	Index     string `json:"index"`
	Worktree  string `json:"worktree"`
	Untracked bool   `json:"untracked"`
}

// handleFSChanges aggregates `git status` across every working copy under
// ~/repos, returning home-relative paths so the Console can show "changed files
// only" alongside the home tree.
func handleFSChanges(w http.ResponseWriter, r *http.Request) {
	root := gitx.ReposRoot()
	ents, err := os.ReadDir(root)
	if err != nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"changes": []fileChange{}})
		return
	}
	out := []fileChange{}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		repo := e.Name()
		dir := filepath.Join(root, repo)
		if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
			continue // not a git working copy
		}
		cs, err := gitx.GitChanges(dir)
		if err != nil {
			continue
		}
		for _, c := range cs {
			out = append(out, fileChange{
				Repo: repo, Path: "repos/" + repo + "/" + c.Path,
				Index: c.Index, Worktree: c.Worktree, Untracked: c.Untracked,
			})
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"changes": out})
}

func emptyMarks() map[string]any {
	return map[string]any{"added": []int{}, "modified": []int{}, "deleted": []int{}}
}

// handleFSLineMarks returns gutter marks for a working-tree file under ~/repos,
// computed against HEAD: which new-file lines are added/modified and where
// deletions occurred. Untracked files mark every line added. Non-repo paths (or
// clean files) return empty marks. The Console overlays these in the viewer.
func handleFSLineMarks(w http.ResponseWriter, r *http.Request) {
	full, rel, ok := safeBrowsePath(r.URL.Query().Get("path"))
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	parts := strings.SplitN(filepath.ToSlash(rel), "/", 3)
	if len(parts) < 3 || parts[0] != "repos" {
		httpx.WriteJSON(w, http.StatusOK, emptyMarks())
		return
	}
	repo, inrepo := parts[1], parts[2]
	dir := filepath.Join(gitx.ReposRoot(), repo)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		httpx.WriteJSON(w, http.StatusOK, emptyMarks())
		return
	}
	// Untracked file => the whole thing is "added".
	if st, _ := gitx.Run(dir, "status", "--porcelain", "--", inrepo); strings.HasPrefix(st, "??") {
		n := countFileLines(full)
		added := make([]int, 0, n)
		for i := 1; i <= n; i++ {
			added = append(added, i)
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"added": added, "modified": []int{}, "deleted": []int{}})
		return
	}
	// --unified=0: hunks are pure deletion/addition runs (no context) => simple map.
	out, err := gitx.Run(dir, "diff", "HEAD", "--unified=0", "--", inrepo)
	if err != nil {
		httpx.WriteJSON(w, http.StatusOK, emptyMarks())
		return
	}
	added, modified, deleted := parseDiffMarks(out)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"added": added, "modified": modified, "deleted": deleted})
}

var hunkRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)`)

// parseDiffMarks reads a `git diff --unified=0` and classifies new-file lines: a
// run of N deletions immediately followed by M additions yields min(N,M)
// "modified" lines (+ any surplus additions as "added"); a deletion run with no
// following addition records a "deleted" marker at the new-file position.
func parseDiffMarks(diff string) (added, modified, deleted []int) {
	added, modified, deleted = []int{}, []int{}, []int{}
	newLine := 0
	pendingDel := 0
	flushDel := func(at int) {
		if pendingDel > 0 {
			deleted = append(deleted, at)
			pendingDel = 0
		}
	}
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			flushDel(newLine)
			if m := hunkRe.FindStringSubmatch(line); m != nil {
				newLine, _ = strconv.Atoi(m[1])
			}
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			// file headers — ignore
		case strings.HasPrefix(line, "+"):
			if pendingDel > 0 {
				modified = append(modified, newLine)
				pendingDel--
			} else {
				added = append(added, newLine)
			}
			newLine++
		case strings.HasPrefix(line, "-"):
			pendingDel++
		default:
			flushDel(newLine)
		}
	}
	flushDel(newLine)
	return
}

// countFileLines counts lines in a file (final line without a trailing newline
// still counts). Used to mark every line of an untracked file as added.
func countFileLines(path string) int {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return 0
	}
	n := strings.Count(string(b), "\n")
	if b[len(b)-1] != '\n' {
		n++ // last line has no trailing newline
	}
	return n
}
