package main

import (
	"bufio"
	"context"
	"net/http"
	"os"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// Recursive filename search for the Console file browser (the FILES improvements): the
// tree endpoint (handleFSTree) is one level deep, so the rail's filter could only
// match already-expanded rows. This walks the whole subtree so a query matches
// every file under the root — not just what's loaded.
//
// It shells out to ripgrep (`rg --files`), which is in the workspace image and
// honours each repo's .gitignore, so node_modules / build output are skipped for
// free (a naive filepath.WalkDir would descend into them). Results are still run
// through the same denylist and capped + time-bounded so a huge tree can't hang
// or flood the response.

const (
	fsSearchDefaultLimit = 500
	fsSearchMaxLimit     = 2000
	fsSearchTimeout      = 5 * time.Second
)

// handleFSSearch: GET /fs/search?path=<root>&q=<query>&limit=<n>
// Returns home-relative paths of files under <root> whose path (relative to the
// root) contains <query>, case-insensitively.
func handleFSSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	qPath := r.URL.Query().Get("path")
	full, rel, ok := safeBrowsePath(qPath)
	if !ok || isCodexGeneratedImagesPath(full) || !fsQueryResolvedOK(qPath, full) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	if q == "" {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"results": []string{}, "truncated": false})
		return
	}
	if fi, err := os.Stat(full); err != nil || !fi.IsDir() {
		httpx.WriteErr(w, http.StatusNotFound, "not_dir", "cannot search: "+rel)
		return
	}
	limit := fsSearchDefaultLimit
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		if n > fsSearchMaxLimit {
			n = fsSearchMaxLimit
		}
		limit = n
	}

	ctx, cancel := context.WithTimeout(r.Context(), fsSearchTimeout)
	defer cancel()
	// --files: list files, honouring .gitignore per repo. --sort path: deterministic
	// order so the capped slice is the alphabetically-first matches. -g '!.git':
	// never descend into git internals (still shown by --hidden otherwise).
	args := []string{"--files", "--hidden", "--sort", "path", "-g", "!.git"}
	if rel == "" {
		// Home search is intentionally the non-working-copy scope. Avoid the
		// large package/cache trees that are useful on disk but noisy in a file
		// picker; repos has its own explicit search scope in the Console.
		for _, glob := range []string{"!repos", "!.cache", "!.local", "!.npm", "!.gradle", "!.cargo/registry", "!.rustup"} {
			args = append(args, "-g", glob)
		}
	}
	cmd := exec.CommandContext(ctx, "rg", args...)
	cmd.Dir = full
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"results": []string{}, "truncated": false})
		return
	}
	if err := cmd.Start(); err != nil {
		// rg missing / unstartable: degrade to an empty result rather than 500.
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"results": []string{}, "truncated": false})
		return
	}

	ql := strings.ToLower(q)
	results := []string{}
	truncated := false
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text() // path relative to `full`, slash-separated
		if line == "" || !strings.Contains(strings.ToLower(line), ql) {
			continue
		}
		// home-relative path: what the tree rows and FileView use to open a file.
		homeRel := line
		if rel != "" {
			homeRel = path.Join(rel, line)
		}
		if isDenied(homeRel) {
			continue
		}
		results = append(results, homeRel)
		if len(results) >= limit {
			truncated = true
			break
		}
	}
	cancel() // stop rg once we've collected enough / hit the timeout
	_ = cmd.Wait()
	sort.Strings(results)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"results": results, "truncated": truncated, "root": browseRoot()})
}
