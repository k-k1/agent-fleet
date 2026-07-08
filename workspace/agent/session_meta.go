package main

// セッションメタ（sessionMeta）の永続化: 保存先ディレクトリと読み書き・列挙・開始ブランチ更新。
// session.go からの機械的分割（docs/23 P1-W4）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// sessionsMetaDir lives in the home volume (persists across Stop→Start) under the
// denylisted .config/agent-fleet, so stopped sessions survive a Workspace restart.
func sessionsMetaDir() string {
	return envOr("AF_SESSIONS_DIR", filepath.Join(homeDir(), ".config", "agent-fleet", "sessions"))
}

func sessionMetaPath(name string) string { return filepath.Join(sessionsMetaDir(), name+".json") }

func writeSessionMeta(m sessionMeta) {
	if err := os.MkdirAll(sessionsMetaDir(), 0o700); err != nil {
		return
	}
	if b, err := json.Marshal(m); err == nil {
		_ = os.WriteFile(sessionMetaPath(m.Name), b, 0o600)
	}
}

func readSessionMeta(name string) (sessionMeta, bool) {
	var m sessionMeta
	b, err := os.ReadFile(sessionMetaPath(name))
	if err != nil {
		return m, false
	}
	if json.Unmarshal(b, &m) != nil {
		return m, false
	}
	return m, true
}

func removeSessionMeta(name string) { _ = os.Remove(sessionMetaPath(name)) }

func listSessionMetas() []sessionMeta {
	ents, err := os.ReadDir(sessionsMetaDir())
	if err != nil {
		return nil
	}
	var out []sessionMeta
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if m, ok := readSessionMeta(strings.TrimSuffix(e.Name(), ".json")); ok {
			out = append(out, m)
		}
	}
	return out
}

// updateSessionStartBranch rewrites the recorded start branch (sessionMeta.Branch) for
// every session whose cwd is at or under dir, after an intentional `git branch -m` on
// that working copy — so the rename isn't mistaken for branch drift (③). Only touches
// metas that carry a start branch; leaves pre-existing ("") ones alone.
func updateSessionStartBranch(dir, branch string) {
	for _, m := range listSessionMetas() {
		if m.Branch == "" || m.Branch == branch {
			continue
		}
		if m.Dir == dir || strings.HasPrefix(m.Dir, dir+string(os.PathSeparator)) {
			m.Branch = branch
			writeSessionMeta(m)
		}
	}
}
