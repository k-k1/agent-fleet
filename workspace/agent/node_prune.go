package main

// node_prune.go — removes Node.js patches that install-node put in the nvm dir once a
// newer patch of the same major is there.
//
// The entrypoint runs install-node for the selected major at every container start, and
// each new patch lands next to the old ones (~200 MB each), so a long-lived home kept
// every patch it ever had.
//
// What keeps this from taking something away:
//   - Only trees carrying nodeInstallMarker go. A patch the member installed with nvm has
//     no marker. The marker only says who installed a tree, not that nobody uses it: nvm
//     reuses an existing tree as is, so a Fleet-installed patch can still be the one a
//     member pinned. Hence the next rule.
//   - Pinned versions are kept whatever their marker: the tree the entrypoint is about to
//     put on PATH (--keep), an exact version saved as the selection (the entrypoint runs
//     `nvm use` on it under set -e, so removing it would stop the start), and exact
//     versions named by an nvm alias or by ~/.nvmrc / ~/repos/*/.nvmrc.
//   - The newest tree must be a working node of its own: its bin/node resolves to an
//     executable regular file inside that tree. Otherwise the major is left alone, and
//     a tree another tree's bin/node resolves into is never removed.
//   - It runs from the entrypoint, before any session exists. A session started earlier
//     has the old patch's bin on PATH, so a Console update leaves the old tree for the
//     next start instead of deleting it under a running process.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// nodeInstallMarker is the file installNode writes into every tree it installs.
const nodeInstallMarker = ".agent-fleet-installed"

// pruneSupersededNodes removes, per major, each marked patch that is older than the
// newest installed patch of that major, except the pinned ones (keep holds directory
// names such as "v22.23.4", on top of pinnedNodeVersions). It returns the removed
// directory names.
func pruneSupersededNodes(keep []string) ([]string, error) {
	root := nvmNodeRoot()
	ents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	pinned := pinnedNodeVersions()
	for _, k := range keep {
		k = filepath.Clean(k)
		if filepath.Base(k) == "bin" { // install-node prints <tree>/bin
			k = filepath.Dir(k)
		}
		pinned[filepath.Base(k)] = true
	}
	type tree struct {
		name   string
		dir    string // symlinks resolved
		ver    []int
		marked bool
		node   string // bin/node with symlinks resolved; "" when it does not resolve
		ok     bool   // node is an executable regular file
	}
	var trees []*tree
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "v") {
			continue
		}
		v := parseDotted(strings.TrimPrefix(e.Name(), "v"))
		if len(v) == 0 {
			continue
		}
		dir := filepath.Join(root, e.Name())
		// A tree without bin/node (a half-removed or foreign directory) neither counts
		// as the newest nor gets removed.
		if _, err := os.Lstat(filepath.Join(dir, "bin", "node")); err != nil {
			continue
		}
		t := &tree{name: e.Name(), dir: dir, ver: v}
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			t.dir = real
		}
		_, merr := os.Stat(filepath.Join(dir, nodeInstallMarker))
		t.marked = merr == nil
		if real, err := filepath.EvalSymlinks(filepath.Join(dir, "bin", "node")); err == nil {
			t.node = real
			if fi, err := os.Stat(real); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
				t.ok = true
			}
		}
		trees = append(trees, t)
	}
	// owner is the tree a resolved path lies in, or nil.
	owner := func(p string) *tree {
		for _, t := range trees {
			if strings.HasPrefix(p, t.dir+string(filepath.Separator)) {
				return t
			}
		}
		return nil
	}
	// Any tree some other tree's node resolves into stays.
	depended := map[string]bool{}
	for _, t := range trees {
		if o := owner(t.node); o != nil && o != t {
			depended[o.name] = true
		}
	}
	byMajor := map[int][]*tree{}
	for _, t := range trees {
		byMajor[t.ver[0]] = append(byMajor[t.ver[0]], t)
	}
	var removed []string
	var firstErr error
	for _, group := range byMajor {
		newest := group[0]
		for _, t := range group[1:] {
			if compareDotted(t.ver, newest.ver) > 0 {
				newest = t
			}
		}
		if !newest.ok || owner(newest.node) != newest {
			continue
		}
		for _, t := range group {
			if !t.marked || t == newest || pinned[t.name] || depended[t.name] {
				continue
			}
			if err := os.RemoveAll(filepath.Join(root, t.name)); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			removed = append(removed, t.name)
		}
	}
	return removed, firstErr
}

// pinnedNodeVersions collects the exact versions ("v22.23.4") something asks for: the
// saved selection, nvm aliases, and .nvmrc files in home and directly under ~/repos.
// A bare major ("22", "lts/*") pins nothing: any patch of it satisfies it.
func pinnedNodeVersions() map[string]bool {
	out := map[string]bool{}
	add := func(s string) {
		s = strings.TrimPrefix(strings.TrimSpace(s), "v")
		if v := parseDotted(s); len(v) >= 3 {
			out["v"+s] = true
		}
	}
	add(readToolchains().Node)
	home := homeDir()
	_ = filepath.WalkDir(filepath.Join(home, ".nvm", "alias"), func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if b, rerr := os.ReadFile(p); rerr == nil {
				add(firstLine(b))
			}
		}
		return nil
	})
	rcs := []string{filepath.Join(home, ".nvmrc")}
	if m, err := filepath.Glob(filepath.Join(home, "repos", "*", ".nvmrc")); err == nil {
		rcs = append(rcs, m...)
	}
	for _, p := range rcs {
		if b, err := os.ReadFile(p); err == nil {
			add(firstLine(b))
		}
	}
	return out
}

func firstLine(b []byte) string {
	s, _, _ := strings.Cut(string(b), "\n")
	return s
}

// runPruneNodeVersions is `workspace-agent prune-node-versions`, which the entrypoint
// runs right after install-node.
// --keep <dir> (repeatable) names a tree that must stay, e.g. the one install-node
// just returned.
func runPruneNodeVersions(args []string) {
	var keep []string
	for len(args) > 0 {
		if args[0] != "--keep" || len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: workspace-agent prune-node-versions [--keep <dir>]...")
			os.Exit(2)
		}
		keep = append(keep, args[1])
		args = args[2:]
	}
	removed, err := pruneSupersededNodes(keep)
	for _, name := range removed {
		fmt.Fprintf(os.Stderr, "[prune-node] removed superseded %s\n", name)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "[prune-node]", err)
		os.Exit(1)
	}
}
