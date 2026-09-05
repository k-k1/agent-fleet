package agents

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// SidStore maps our deterministic slot sid to an agent's own session id, so a slot
// resumes its OWN conversation (a thin read face over internal/fstore's Store: Read
// swallows the ok flag and returns "" — callers treat the empty string as "none").
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
