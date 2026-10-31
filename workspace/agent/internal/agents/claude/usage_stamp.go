package claude

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

var _ agents.UsageSourcer = agentImpl{}

// UsageSource implements agents.UsageSourcer. One jsonlPaths lookup serves both the stamp (path,
// size and mtime of every sibling UsageTurns can choose between) and the read, so the stamp
// describes the files the turns came from. A session with no transcript gets a source with no
// stamp and no turns, and costs no second search.
func (agentImpl) UsageSource(m session.Meta) (agents.UsageSource, bool) {
	paths := jsonlPaths(session.UUID(m.Dir, m.Name))
	if len(paths) == 0 {
		return agents.UsageSource{Turns: func() ([]transcript.Turn, bool) { return nil, true }}, true
	}
	type file struct {
		path string
		mod  time.Time
	}
	files := make([]file, 0, len(paths))
	var sb strings.Builder
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			return agents.UsageSource{}, false // a stale memo entry: let the plain read search again
		}
		files = append(files, file{p, fi.ModTime()})
		fmt.Fprintf(&sb, "%s\x00%d\x00%d\x00", p, fi.Size(), fi.ModTime().UnixNano())
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	ordered := make([]string, len(files))
	for i, f := range files {
		ordered[i] = f.path
	}
	return agents.UsageSource{
		Stamp: sb.String(),
		Turns: func() ([]transcript.Turn, bool) {
			lines, _, ok := readConversation(ordered)
			return CollectUsageTurns(lines), ok
		},
	}, true
}
