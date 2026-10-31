package codex

import (
	"fmt"
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

var _ agents.UsageSourcer = agentImpl{}

// UsageSource implements agents.UsageSourcer: the rollout's path, size and mtime (found through
// the memoized rolloutPath), and Transcript's turns, reported not-ok when the shared parse could
// not be brought up to date and a cached one was served instead.
func (agentImpl) UsageSource(m session.Meta) (agents.UsageSource, bool) {
	p := rolloutPath(sids.Read(session.UUID(m.Dir, m.Name)))
	if p == "" {
		return agents.UsageSource{}, false
	}
	fi, err := os.Stat(p)
	if err != nil {
		return agents.UsageSource{}, false
	}
	return agents.UsageSource{
		Stamp: fmt.Sprintf("%s\x00%d\x00%d", p, fi.Size(), fi.ModTime().UnixNano()),
		Turns: func() ([]transcript.Turn, bool) {
			td, fresh := readTranscriptFresh(m)
			return td.Turns, fresh
		},
	}, true
}
