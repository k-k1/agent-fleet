package session

// Persistence of the session Meta: the directory it lives in, read/write, enumeration and
// updating the starting branch.

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fleetgraph"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// MetaDir lives in the home volume (persists across Stop→Start) under the denylisted
// .local/state/agent-fleet, so stopped sessions survive a Workspace restart.
//
// It used to say the same sentence about .config/agent-fleet, and on the ecs-ec2 runtime
// that was not true: ~/.config is one of AF_WS_KEEP_DIRS, so the ledger was on EFS and
// ListMetas below — one ReadDir plus one ReadFile per session, 5M+4 file syscalls, so 1,039
// at 207 sessions — ran over NFS once every four seconds per open Console tab (ADR 0087
// source A). (The 836 this comment used to quote was measured with a trace set that left out
// fstat, which os.ReadFile's f.Stat() emits once per file on linux/amd64; re-measured
// 2026-09-18.)
// paths.AgentStateDir is the home-volume root that keeps the sentence true.
func MetaDir() string {
	if v := os.Getenv("AF_SESSIONS_DIR"); v != "" {
		return v
	}
	return filepath.Join(paths.AgentStateDir(), "sessions")
}

func MetaPath(name string) string { return filepath.Join(MetaDir(), name+".json") }

func WriteMeta(m Meta) { _ = WriteMetaChecked(m) }

// WriteMetaChecked is WriteMeta for a caller that has to know it worked — the cleanup
// restore, which must not report a session as back when its meta never reached the disk.
func WriteMetaChecked(m Meta) error {
	if err := os.MkdirAll(MetaDir(), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.WriteFile(MetaPath(m.Name), b, 0o600); err != nil {
		return err
	}
	rememberCWD(m)
	return nil
}

func ReadMeta(name string) (Meta, bool) {
	var m Meta
	b, err := os.ReadFile(MetaPath(name))
	if err != nil {
		return m, false
	}
	if json.Unmarshal(b, &m) != nil {
		return m, false
	}
	rememberCWD(m)
	return m, true
}

// RemoveMeta forgets the meta and NOTHING else — no fleet-graph lineage change. It has
// exactly ONE production caller: the stopped-session TTL auto-prune in
// HandleListSessions, which is the one case ADR 0096 decision 6 says must NOT erase the
// lineage row (prune already costs the session's card; costing its line in the graph too
// is the exact regression the ADR's ledger exists to undo — see docs/log/94). Every other
// place a session's meta is forgotten is a PERSON asking for it to be gone, and belongs on
// RemoveMetaAndLineage instead. meta_remove_sites_test.go counts callers of both so a new
// call site cannot silently pick the wrong one.
func RemoveMeta(name string) { _ = os.Remove(MetaPath(name)) }

// RemoveMetaAndLineage is RemoveMeta plus erasing the session's fleet-graph lineage row
// (ADR 0096 decision 6: an explicit, person-initiated forgetting of a session means
// "deleted", not "still has a line in the graph"). Use this — never bare RemoveMeta — for
// every path that forgets a meta because someone asked to: /stop, DELETE /sessions/{name}
// (with or without ?reclaim=1), and a working-copy delete's session collateral. Erasure is
// best-effort and logged, never fatal: the meta is already gone by the time this runs, so
// failing the request over it would be strictly worse than a leftover lineage row.
func RemoveMetaAndLineage(name string) {
	RemoveMeta(name)
	if err := fleetgraph.EraseLineage(name); err != nil {
		log.Printf("fleet-graph: erase lineage for %s: %v", name, err)
	}
	// Drop the in-process dedup state (last-observed live state / conv id) too — a deleted
	// name is never reused (session-slug-immutable-managed-no-env), so without this it
	// would just sit unused in the map for the rest of the process's life.
	fleetgraph.ForgetSession(name)
}

func ListMetas() []Meta {
	ents, err := os.ReadDir(MetaDir())
	if err != nil {
		return nil
	}
	var out []Meta
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if m, ok := ReadMeta(strings.TrimSuffix(e.Name(), ".json")); ok {
			out = append(out, m)
		}
	}
	return out
}

// UpdateStartBranch rewrites the recorded start branch (Meta.Branch) for
// every session whose cwd is at or under dir, after an intentional `git branch -m` on
// that working copy — so the rename isn't mistaken for branch drift (③). Only touches
// metas that carry a start branch; leaves pre-existing ("") ones alone.
func UpdateStartBranch(dir, branch string) {
	for _, m := range ListMetas() {
		if m.Branch == "" || m.Branch == branch {
			continue
		}
		if m.Dir == dir || strings.HasPrefix(m.Dir, dir+string(os.PathSeparator)) {
			m.Branch = branch
			WriteMeta(m)
		}
	}
}
