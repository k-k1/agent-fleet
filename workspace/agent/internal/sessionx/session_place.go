package sessionx

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/spacex"
)

// PlaceOf decides where a session whose working directory is dir runs. It is the one place
// that classifies a directory: a create, a recreate and a fork store its answer in the meta,
// and nothing re-derives it from the folder name afterwards, because a space and a repository
// can share a basename. A space is told by spacex.FolderOf (its marker under ~/spaces); home
// by being the home directory itself; a repository or worktree by being a real directory
// directly under ~/repos under any folder name (a symlink entry never: GET /repos skips those
// too). Anything else is PlaceOther. Never nil.
//
// The parent is compared physically: a spelling with ".." is resolved before it is judged, so
// ~/repos/link/../app is filed where it really runs, never as ~/repos/app.
func PlaceOf(dir string) *session.Place {
	other := &session.Place{Kind: session.PlaceOther}
	if dir == "" || !filepath.IsAbs(dir) {
		return other
	}
	if f := spacex.FolderOf(dir); f != "" {
		return &session.Place{Kind: session.PlaceSpace, ID: f}
	}
	if sameDir(dir, homeDir()) {
		return &session.Place{Kind: session.PlaceHome}
	}
	parent, folder, ok := splitPhysical(dir)
	if !ok {
		return other
	}
	root := gitx.ReposRoot()
	if !sameDir(parent, root) {
		return other
	}
	full := filepath.Join(parent, folder)
	if fi, err := os.Lstat(full); err != nil || !fi.IsDir() {
		return other // missing, a file, or a symlink entry, which GET /repos skips too
	}
	if main, linked := gitx.LinkedWorktreeMain(full); linked {
		p := &session.Place{Kind: session.PlaceWorktree, ID: folder}
		if main != "" && sameDir(filepath.Dir(main), root) {
			p.Parent = filepath.Base(main)
		}
		return p
	}
	return &session.Place{Kind: session.PlaceRepo, ID: folder}
}

// sessionPlaceOf is the place of a session in dir with subdir, judged on the directory its
// process enters by the rule Meta.CWD launches with: an existing subdir is Joined, which cleans
// `link/..` lexically, so the cleaned dir is the root; without one (or with one that is gone,
// where CWD falls back to the raw dir) the raw dir is entered.
func sessionPlaceOf(dir, subdir string) *session.Place {
	if subdir != "" && dir != "" && session.DirExists(filepath.Join(dir, filepath.FromSlash(subdir))) {
		return PlaceOf(filepath.Clean(dir))
	}
	return PlaceOf(dir)
}

// repoName is what a meta records in Repo for a session in dir placed at p: the directory's base
// name, except for a space or the home directory, which are no working copy. Without the
// exception a space would pass for the repository that shares its folder name, and a home
// session for a repository named after the member.
func repoName(p *session.Place, dir string) string {
	if p != nil && (p.Kind == session.PlaceSpace || p.Kind == session.PlaceHome) {
		return ""
	}
	return filepath.Base(dir)
}

// splitPhysical splits dir into the directory holding its last element and that element,
// without letting filepath.Clean fold a ".." across a symlink: a parent spelt with ".." is
// resolved, and a last element of ".." is resolved whole. Trailing "/" and "/." name the
// element before them.
func splitPhysical(dir string) (parent, last string, ok bool) {
	p := trimSelf(dir)
	last = filepath.Base(p)
	if last == ".." {
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			return "", "", false
		}
		return filepath.Dir(real), filepath.Base(real), true
	}
	if last == "/" || last == "." {
		return "", "", false
	}
	parent = strings.TrimRight(p[:len(p)-len(last)], "/")
	if parent == "" {
		parent = "/"
	}
	if filepath.Clean(parent) != parent {
		real, err := filepath.EvalSymlinks(parent)
		if err != nil {
			return "", "", false
		}
		parent = real
	}
	return parent, last, true
}

// trimSelf drops trailing "/" and "/." elements, which name the directory before them.
func trimSelf(p string) string {
	for len(p) > 1 {
		switch {
		case strings.HasSuffix(p, "/"):
			p = p[:len(p)-1]
		case strings.HasSuffix(p, "/."):
			p = p[:len(p)-1] // the "/" before it may be the root's own
		default:
			return p
		}
	}
	return p
}

// sameDir reports whether a and b name one directory. Clean spellings are compared as they
// are first; otherwise both are resolved, so a symlinked home or ~/repos still matches and a
// ".." after a symlink is followed where it really leads.
func sameDir(a, b string) bool {
	a, b = trimSelf(a), trimSelf(b)
	if a == b && filepath.Clean(a) == a {
		return true
	}
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	return err == nil && ra == rb
}
