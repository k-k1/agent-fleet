package sessionx

import (
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/spacex"
)

// PlaceOf decides where a session whose working directory is dir runs. It is the one place
// that classifies a directory: a create, a recreate and a fork store its answer in the meta,
// and nothing re-derives it from the folder name afterwards, because a space and a repository
// can share a basename. A space is told by spacex.FolderOf (its marker under
// ~/spaces), a repository or worktree by sitting directly under ~/repos, home by being the
// home directory itself; anything else is PlaceOther. Never nil.
func PlaceOf(dir string) *session.Place {
	if dir == "" || !filepath.IsAbs(dir) {
		return &session.Place{Kind: session.PlaceOther}
	}
	if f := spacex.FolderOf(dir); f != "" {
		return &session.Place{Kind: session.PlaceSpace, ID: f}
	}
	clean := filepath.Clean(dir)
	if sameDir(clean, homeDir()) {
		return &session.Place{Kind: session.PlaceHome}
	}
	root := gitx.ReposRoot()
	if !sameDir(filepath.Dir(clean), root) {
		return &session.Place{Kind: session.PlaceOther}
	}
	folder := filepath.Base(clean)
	if _, ok := gitx.ResolveRepoDir(folder); !ok {
		return &session.Place{Kind: session.PlaceOther}
	}
	if main, ok := gitx.LinkedWorktreeMain(clean); ok {
		p := &session.Place{Kind: session.PlaceWorktree, ID: folder}
		if sameDir(filepath.Dir(main), root) {
			p.Parent = filepath.Base(main)
		}
		return p
	}
	return &session.Place{Kind: session.PlaceRepo, ID: folder}
}

// sameDir reports whether a and b name one directory, comparing the cleaned spellings first
// and the resolved paths only when those differ, so a symlinked home or ~/repos still matches.
func sameDir(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	return err == nil && ra == rb
}
