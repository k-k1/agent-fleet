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
//   - Trees must be self-contained. A major is left alone when any of its entries is a
//     symlink, or when any symlink inside one of its trees points outside that tree:
//     following chains to decide what still depends on what proved error-prone (a link
//     through the old tree and back passed a final-target check), and the trees Node
//     ships only link within themselves. The newest must also have an executable bin/node.
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
		ver    []int
		marked bool
		link   bool // the entry itself is a symlink
	}
	byMajor := map[int][]*tree{}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), "v") {
			continue
		}
		v := parseDotted(strings.TrimPrefix(e.Name(), "v"))
		if len(v) == 0 {
			continue
		}
		t := &tree{name: e.Name(), ver: v, link: e.Type()&os.ModeSymlink != 0}
		if !t.link && !e.IsDir() {
			continue
		}
		_, merr := os.Lstat(filepath.Join(root, e.Name(), nodeInstallMarker))
		t.marked = merr == nil
		byMajor[v[0]] = append(byMajor[v[0]], t)
	}
	var removed []string
	var firstErr error
	for _, group := range byMajor {
		safe := true
		for _, t := range group {
			if t.link || linksOutside(filepath.Join(root, t.name)) {
				safe = false
				break
			}
		}
		if !safe {
			continue
		}
		var newest *tree
		for _, t := range group {
			if newest == nil || compareDotted(t.ver, newest.ver) > 0 {
				newest = t
			}
		}
		if !executableNode(filepath.Join(root, newest.name)) {
			continue
		}
		for _, t := range group {
			if !t.marked || t == newest || pinned[t.name] {
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

// linksOutside reports whether any symlink under dir points outside dir (judged on the
// link text, joined to the link's directory), or whether dir cannot be fully read. When
// in doubt it answers true, which keeps the whole major.
func linksOutside(dir string) bool {
	prefix := filepath.Clean(dir) + string(filepath.Separator)
	outside := false
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			outside = true
			return filepath.SkipAll
		}
		if d.Type()&os.ModeSymlink == 0 {
			return nil
		}
		target, rerr := os.Readlink(p)
		if rerr != nil {
			outside = true
			return filepath.SkipAll
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(p), target)
		}
		if !strings.HasPrefix(filepath.Clean(target)+string(filepath.Separator), prefix) {
			outside = true
			return filepath.SkipAll
		}
		return nil
	})
	return outside || err != nil
}

// executableNode reports whether dir/bin/node is an executable regular file (in-tree
// links are allowed; linksOutside already rejected the others).
func executableNode(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, "bin", "node"))
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
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
	readAliases(filepath.Join(home, ".nvm", "alias"), map[string]bool{}, 0, func(b []byte) { add(firstLine(b)) })
	rcs := []string{filepath.Join(home, ".nvmrc")}
	if m, err := filepath.Glob(filepath.Join(home, "repos", "*", ".nvmrc")); err == nil {
		rcs = append(rcs, m...)
	}
	for _, p := range rcs {
		if b, err := os.ReadFile(p); err == nil {
			add(nvmrcVersion(b))
		}
	}
	return out
}

// readAliases calls fn with the content of every alias file under dir. Like nvm, which
// reads them with a plain file test, it follows symlinks — to files and to directories
// (dotfile managers link them) — and the visited set stops a link cycle.
func readAliases(dir string, seen map[string]bool, depth int, fn func([]byte)) {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil || seen[real] || depth > 8 {
		return
	}
	seen[real] = true
	ents, err := os.ReadDir(real)
	if err != nil {
		return
	}
	for _, e := range ents {
		p := filepath.Join(real, e.Name())
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if fi.IsDir() {
			readAliases(p, seen, depth+1, fn)
		} else if fi.Mode().IsRegular() {
			if b, err := os.ReadFile(p); err == nil {
				fn(b)
			}
		}
	}
}

// nvmrcVersion reads the version a .nvmrc asks for the way nvm 0.40 does: comments
// ("#" to end of line) and blank lines are dropped, key=value lines are options, and
// the first remaining line is the version.
func nvmrcVersion(b []byte) string {
	for _, line := range strings.Split(string(b), "\n") {
		line, _, _ = strings.Cut(line, "#")
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "=") {
			continue
		}
		return line
	}
	return ""
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
