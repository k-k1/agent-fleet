package claude

import (
	"fmt"
	"os"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var _ agents.UsageStamper = agentImpl{}

// UsageStamp is the path, size and mtime of every sibling jsonl UsageTurns can choose between
// (agents.UsageStamper). The sibling list is part of the stamp because the file that gets read
// flips by mtime; the paths come from the same memoized lookup UsageTurns uses.
func (agentImpl) UsageStamp(m session.Meta) (string, bool) {
	var sb strings.Builder
	for _, p := range jsonlPaths(session.UUID(m.Dir, m.Name)) {
		fi, err := os.Stat(p)
		if err != nil {
			return "", false // a file the parse may still reach is unreadable: say nothing
		}
		fmt.Fprintf(&sb, "%s\x00%d\x00%d\x00", p, fi.Size(), fi.ModTime().UnixNano())
	}
	return sb.String(), sb.Len() > 0
}
