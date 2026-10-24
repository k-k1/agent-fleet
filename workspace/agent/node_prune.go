package main

// node_prune.go — removes Node.js patches that install-node put in the nvm dir once a
// newer patch of the same major is there.
//
// The entrypoint runs install-node for the selected major at every container start, and
// each new patch lands next to the old ones (~200 MB each), so a long-lived home kept
// every patch it ever had.
//
// Two rules keep this from taking something away:
//   - Only trees carrying nodeInstallMarker go. A patch the member installed with nvm
//     (`nvm install 22.9.0`, an exact .nvmrc pin) has no marker and is never touched.
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
// newest installed patch of that major (marked or not). It returns the removed
// directory names.
func pruneSupersededNodes() ([]string, error) {
	root := nvmNodeRoot()
	ents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	type tree struct {
		name   string
		ver    []int
		marked bool
	}
	byMajor := map[int][]tree{}
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
		if _, err := os.Stat(filepath.Join(dir, "bin", "node")); err != nil {
			continue
		}
		_, merr := os.Stat(filepath.Join(dir, nodeInstallMarker))
		byMajor[v[0]] = append(byMajor[v[0]], tree{name: e.Name(), ver: v, marked: merr == nil})
	}
	var removed []string
	var firstErr error
	for _, trees := range byMajor {
		newest := trees[0]
		for _, t := range trees[1:] {
			if compareDotted(t.ver, newest.ver) > 0 {
				newest = t
			}
		}
		for _, t := range trees {
			if !t.marked || t.name == newest.name {
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

// runPruneNodeVersions is `workspace-agent prune-node-versions`, which the entrypoint
// runs right after install-node.
func runPruneNodeVersions(args []string) {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: workspace-agent prune-node-versions")
		os.Exit(2)
	}
	removed, err := pruneSupersededNodes()
	for _, name := range removed {
		fmt.Fprintf(os.Stderr, "[prune-node] removed superseded %s\n", name)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "[prune-node]", err)
		os.Exit(1)
	}
}
