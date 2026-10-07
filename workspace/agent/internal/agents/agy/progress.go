package agy

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var _ agents.ProgressReporter = agentImpl{}

// StateSourceModTime is the mtime of the file this kind's working verdict is read from
// (agents.ProgressReporter).
func (agentImpl) StateSourceModTime(m session.Meta) (time.Time, bool) {
	conv := sids.Read(session.UUID(m.Dir, m.Name))
	if conv == "" {
		return time.Time{}, false
	}
	return dbModTime(conv)
}
