package session

// セッションメタ（Meta）の永続化: 保存先ディレクトリと読み書き・列挙・開始ブランチ更新。
// package main の session_meta.go からの移設（docs/log/23 残① Wave A）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// MetaDir lives in the home volume (persists across Stop→Start) under the
// denylisted .config/agent-fleet, so stopped sessions survive a Workspace restart.
func MetaDir() string {
	if v := os.Getenv("AF_SESSIONS_DIR"); v != "" {
		return v
	}
	return filepath.Join(paths.HomeDir(), ".config", "agent-fleet", "sessions")
}

func MetaPath(name string) string { return filepath.Join(MetaDir(), name+".json") }

func WriteMeta(m Meta) {
	if err := os.MkdirAll(MetaDir(), 0o700); err != nil {
		return
	}
	if b, err := json.Marshal(m); err == nil {
		_ = os.WriteFile(MetaPath(m.Name), b, 0o600)
	}
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
