// Package pathguard is the one place this repo confines a query path to a root
// directory, lexically AND after symlink resolution. It exists because
// workspace/agent/fs.go's browse-root confinement (safeBrowsePath /
// fsResolvedOKUnder) and the lcpp harness's cwd confinement (ADR 0093 decision 5,
// internal/harness/cwd.go) are the same judgement against two different roots — a
// package.go can't import package main's fs.go, and ADR 0081 decision 3 is explicit
// that the same judgement must not be written twice. Both callers keep their own
// denylist and multi-root logic; this package only answers "is this path inside
// that root," lexically and for real.
package pathguard

import (
	"os"
	"path/filepath"
	"strings"
)

// Resolve joins rel onto root purely lexically: Clean it, then reject any form
// that steps above root ("..", "../x", or — once joined and made relative to root
// again — anything outside it). It does not touch the filesystem, so it cannot see
// a symlink inside root that points back out; a caller that must defend against
// that (any caller resolving a WRITE target, or a read where the content itself
// must not leak) also calls ResolveUnder on the result. rel == "." or "" both mean
// root itself; outRel is always root-relative and OS-separated ("." for root).
func Resolve(root, rel string) (full, outRel string, ok bool) {
	rel = filepath.Clean(rel)
	if rel == "." {
		rel = ""
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", false
	}
	full = filepath.Join(root, rel)
	r, err := filepath.Rel(root, full)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", "", false
	}
	if r == "." {
		r = ""
	}
	return full, r, true
}

// ResolveUnder re-validates full AFTER symlink resolution against root — the check
// a purely lexical join (Resolve) cannot make: a symlink planted inside root that
// points outside it. It walks up to the nearest existing ancestor so a
// not-yet-existing suffix (a file a write/mkdir/rename is about to create) is fine
// as long as every EXISTING path component resolves within root. rel is
// root-relative on success ("." for root itself); ok is false when any existing
// component resolves outside root, or is an unresolvable (dangling or looping)
// symlink.
func ResolveUnder(full, root string) (rel string, ok bool) {
	rroot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	p := filepath.Clean(full)
	suffix := ""
	for {
		r, err := filepath.EvalSymlinks(p)
		if err == nil {
			resolved := filepath.Join(r, suffix)
			relp, rerr := filepath.Rel(rroot, resolved)
			if rerr != nil || relp == ".." || strings.HasPrefix(relp, ".."+string(filepath.Separator)) {
				return "", false
			}
			return relp, true
		}
		if !os.IsNotExist(err) {
			return "", false
		}
		if _, lerr := os.Lstat(p); lerr == nil {
			return "", false // exists but unresolvable: a dangling or looping symlink
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", false
		}
		suffix = filepath.Join(filepath.Base(p), suffix)
		p = parent
	}
}
