package codex

import (
	"fmt"
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var _ agents.UsageStamper = agentImpl{}

// UsageStamp is the rollout's path, size and mtime (agents.UsageStamper): the only file
// Transcript parses for turns, found through the memoized rolloutPath lookup.
func (agentImpl) UsageStamp(m session.Meta) (string, bool) {
	p := rolloutPath(sids.Read(session.UUID(m.Dir, m.Name)))
	if p == "" {
		return "", false
	}
	fi, err := os.Stat(p)
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("%s\x00%d\x00%d", p, fi.Size(), fi.ModTime().UnixNano()), true
}
