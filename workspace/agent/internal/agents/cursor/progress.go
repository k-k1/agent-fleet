package cursor

import (
	"os"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var _ agents.ProgressReporter = agentImpl{}

// StateSourceModTime is the mtime of the file this kind's working verdict is read from
// (agents.ProgressReporter).
func (agentImpl) StateSourceModTime(m session.Meta) (time.Time, bool) {
	chatID := ChatID(m)
	if chatID == "" {
		return time.Time{}, false
	}
	return modTime(transcriptPath(m.Dir, chatID))
}

func modTime(path string) (time.Time, bool) {
	if path == "" {
		return time.Time{}, false
	}
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}
