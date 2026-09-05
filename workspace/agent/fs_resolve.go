package main

// Resolving path references (POST /fs/resolve).
//
// An agent's reply writes where its artifacts are in inline code (`docs/log/65.md`,
// `_act-parts/`, `/home/dev/repos/x/README.md`). The Console's mirror turns those into links
// only when they really exist, so on every render it asks which file a string points at and
// whether it is there. This handler answers that one question.
//
// Why resolve on the server rather than brute-forcing fs/tree from the Console:
//
//   - There is more than one base. A relative path is nominally against that turn's cwd, but
//     agents write against the repository root as well (especially when cwd is a subfolder). The
//     "cwd, then repository root" fallback is more natural here, where a hit or a miss is
//     settled in one round trip, and far cheaper than the Console listing a directory per
//     candidate.
//   - This side is what actually knows the repository root. The Console can only carve
//     "repos/<name>" out of cwd with a regex, while here .git is walked upwards to the right
//     root for a worktree and for a subfolder launch alike (.git is a file in a worktree and a
//     directory in a normal clone).
//   - safeBrowsePath already knows the readable places outside home (scratch, the role-scoped
//     docs mount). The Console has no such knowledge and misreads an absolute path as
//     repository-relative.
//   - Only hits come back. A directory listing (thousands of entries for node_modules) is never
//     carried to the browser just to confirm existence.
//
// Contract: a ref is the path itself. A line number such as `:12` and a trailing slash are
// dropped by the Console before sending (the line number is also what the Console itself needs
// to open a pane). Only what existed appears in resolved, and a ref that is absent reads as "not
// there". path is in the shape the Console's fs API uses (browse-root-relative under home,
// absolute for the scratch / docs mounts).

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

const (
	fsResolveMaxBody = 64 * 1024
	// Cap on the paths one document (one turn's body) may cite. Anything past it is dropped
	// silently: only the links are missing, the body still reads.
	fsResolveMaxRefs = 64
	fsResolveMaxRef  = 512
	// Cap on how far up from cwd .git is looked for. The browse root stops the walk anyway, so
	// this is only belt and braces.
	fsResolveMaxWalk = 24
)

type fsResolveRequest struct {
	Cwd  string   `json:"cwd"`  // that turn's working directory (absolute or browse-relative)
	Refs []string `json:"refs"` // the path strings written in the body
}

type fsResolveEntry struct {
	Path string `json:"path"`
	Type string `json:"type"` // "file" | "dir"
}

func handleFSResolve(w http.ResponseWriter, r *http.Request) {
	var req fsResolveRequest
	if serr := httpx.DecodeStrictJSON(r, &req, fsResolveMaxBody); serr != nil {
		httpx.WriteErr(w, serr.Status, serr.Code, serr.Message)
		return
	}
	refs := req.Refs
	if len(refs) > fsResolveMaxRefs {
		refs = refs[:fsResolveMaxRefs]
	}
	cwd, repo := fsResolveBases(req.Cwd)
	out := map[string]fsResolveEntry{}
	for _, ref := range refs {
		if e, ok := fsResolveRef(ref, cwd, repo); ok {
			out[ref] = e
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"resolved": out})
}

// fsResolveBases turns the request's cwd into the two absolute bases a relative reference
// is tried against: the working directory itself, and the repository root it sits in.
// An unusable cwd yields empty bases — absolute references still resolve.
func fsResolveBases(cwd string) (cwdFull, repoFull string) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return "", ""
	}
	full, _, ok := safeBrowsePath(cwd)
	if !ok {
		return "", ""
	}
	if fi, err := os.Stat(full); err != nil || !fi.IsDir() {
		return "", ""
	}
	return full, fsRepoRoot(full)
}

// fsRepoRoot walks up from dir to the working copy's root — the directory holding .git,
// which is a DIRECTORY in a normal clone and a FILE in a worktree. Bounded by the browse
// root, so it can never answer with somebody's home or /. "" when dir is not in a repo.
func fsRepoRoot(dir string) string {
	root := browseRoot()
	rroot, err := filepath.EvalSymlinks(root)
	if err != nil {
		rroot = root
	}
	cur := dir
	for i := 0; i < fsResolveMaxWalk; i++ {
		if _, err := os.Lstat(filepath.Join(cur, ".git")); err == nil {
			return cur
		}
		if cur == root || cur == rroot {
			return ""
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
	return ""
}

// fsResolveRef answers what one written reference points at, trying the plausible readings
// in order and taking the first that exists:
//
//	~/x        → home. One reading only; the user wrote where they meant.
//	/x         → the real absolute path first (that is what an agent citing a file writes),
//	             then repository-root-relative (`/docs/a.md` — how repository Markdown links
//	             its own tree).
//	x, a/b     → the turn's cwd first (the overwhelmingly common case), then the repository
//	             root — which is what a reply written from a subfolder, or one quoting a
//	             path out of a repo-root-relative document, actually means.
//
// Every candidate goes through safeBrowsePath, so the denylist, the browse root and the
// read-only roots decide what may be answered about at all.
func fsResolveRef(ref, cwdFull, repoFull string) (fsResolveEntry, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || len(ref) > fsResolveMaxRef || strings.ContainsRune(ref, 0) {
		return fsResolveEntry{}, false
	}
	var cands []string
	switch {
	case strings.HasPrefix(ref, "~/"):
		cands = append(cands, filepath.Join(homeDir(), ref[2:]))
	case filepath.IsAbs(ref):
		cands = append(cands, filepath.Clean(ref))
		if repoFull != "" {
			cands = append(cands, filepath.Join(repoFull, ref))
		}
	default:
		if cwdFull != "" {
			cands = append(cands, filepath.Join(cwdFull, ref))
		}
		if repoFull != "" && repoFull != cwdFull {
			cands = append(cands, filepath.Join(repoFull, ref))
		}
	}
	for _, cand := range cands {
		full, rel, ok := safeBrowsePath(cand)
		if !ok || !fsQueryResolvedOK(cand, full) {
			continue
		}
		fi, err := os.Stat(full)
		if err != nil {
			continue
		}
		typ := "file"
		if fi.IsDir() {
			typ = "dir"
		}
		return fsResolveEntry{Path: rel, Type: typ}, true
	}
	return fsResolveEntry{}, false
}
