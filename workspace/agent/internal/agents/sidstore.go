package agents

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// SidStore maps our deterministic slot sid to an agent's own session id, so a slot
// resumes its OWN conversation (internal/fstore の Store に薄い読み口を被せたもの:
// Read は ok を潰して "" を返す — 呼び出し側は空文字を「無し」として扱う)。
// docs/log/23 残① Wave D: package main の sidStore を CLI 縦割りパッケージから使える
// よう移設・汎用化した。
type SidStore struct{ files fstore.Store[string] }

// NewSidStore builds a SidStore persisting under <AgentConfigDir>/<subdir>.
func NewSidStore(subdir string) SidStore {
	return SidStore{fstore.TrimmedStrings(paths.AgentConfigDir, subdir)}
}

func (s SidStore) Read(sid string) string {
	v, _ := s.files.Read(sid)
	return v
}

func (s SidStore) Write(sid, val string) { _ = s.files.Write(sid, val) }
func (s SidStore) Remove(sid string)     { s.files.Remove(sid) }

// Path exposes the backing file path, for callers that must distinguish "stored
// empty value" from "no entry at all" (agy's brain-dir snapshot).
func (s SidStore) Path(sid string) string { return s.files.Path(sid) }
