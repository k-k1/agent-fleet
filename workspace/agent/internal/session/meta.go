package session

// Persistence of the session Meta: the directory it lives in, read/write, enumeration and
// updating the starting branch.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// MetaDir lives in the home volume (persists across Stop→Start) under the denylisted
// .local/state/agent-fleet, so stopped sessions survive a Workspace restart.
//
// It used to say the same sentence about .config/agent-fleet, and on the ecs-ec2 runtime
// that was not true: ~/.config is one of AF_WS_KEEP_DIRS, so the ledger was on EFS and
// ListMetas below — one ReadDir plus one ReadFile per session, 836 file syscalls at 207
// sessions — ran over NFS once every four seconds per open Console tab (ADR 0087 source A).
// paths.AgentStateDir is the home-volume root that keeps the sentence true.
func MetaDir() string {
	if v := os.Getenv("AF_SESSIONS_DIR"); v != "" {
		return v
	}
	return filepath.Join(paths.AgentStateDir(), "sessions")
}

func MetaPath(name string) string { return filepath.Join(MetaDir(), name+".json") }

func WriteMeta(m Meta) {
	if err := os.MkdirAll(MetaDir(), 0o700); err != nil {
		return
	}
	if b, err := json.Marshal(m); err == nil {
		_ = os.WriteFile(MetaPath(m.Name), b, 0o600)
	}
	rememberCWD(m)
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

func RemoveMeta(name string) { _ = os.Remove(MetaPath(name)) }

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
