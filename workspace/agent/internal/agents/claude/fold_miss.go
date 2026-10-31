package claude

import (
	"math/rand"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The usage fold (all metas, archived ones included, once a minute while usage is read) asks for
// a session's main transcript and then for its subagent transcripts. For a session that never
// produced a transcript neither answer is memoized, so each pass costs two sweeps of every
// project directory on the claude mount see docs/decisions/0087-efs-metadata-io.md.
//
// The fold alone may remember "nothing found": it is accounting with a watermark that catches
// up, so a miss only delays the first turn or the first subagent file by the TTL. "No main
// transcript means no subagents directory" is false (see subagentAnchors), and subagentBases
// also feeds BackgroundBusy, the reaper, delivery confirmation and reports, which keep asking
// the disk — which is why this is a separate memo behind a separate entry point, like
// display_miss.go, and not an option on subagentMemo.
//
// An archived session cannot grow, so its miss is trusted for hours; the TTL is not infinite
// because filepath.Glob swallows readdir errors, so a transient EFS failure can be recorded as
// a miss, and the TTL is what heals that. Each archived entry is noted up to half a TTL old, so
// the hundreds noted in one pass do not all expire in the same pass. Archiving or restoring
// flips the flag and discards the remembered miss. Entries are never forgotten; the map is
// bounded by the number of sessions.
const (
	foldMissTTLLive     = 2 * time.Minute
	foldMissTTLArchived = 6 * time.Hour
)

var foldMisses = &missMemo{ttlLive: foldMissTTLLive, ttlStopped: foldMissTTLArchived}

func foldMissKey(m session.Meta) string {
	return ConfigDir() + "\x00" + session.UUID(m.Dir, m.Name)
}

// UsageMissHeld implements agents.UsageMissMemo.
func (agentImpl) UsageMissHeld(m session.Meta) bool {
	return foldMisses.holds(foldMissKey(m), !m.Archived, time.Now())
}

// UsageMissNote implements agents.UsageMissMemo.
func (agentImpl) UsageMissNote(m session.Meta) {
	at := time.Now()
	if m.Archived {
		at = at.Add(-time.Duration(rand.Int63n(int64(foldMissTTLArchived / 2))))
	}
	foldMisses.note(foldMissKey(m), !m.Archived, at)
}

var projectSweepCount atomic.Int64

// ProjectSweeps counts the Glob sweeps over projects/* made so far; tests use it to prove a
// path costs no sweep.
func ProjectSweeps() int64 { return projectSweepCount.Load() }

func sweepProjects(pattern ...string) []string {
	projectSweepCount.Add(1)
	m, _ := filepath.Glob(filepath.Join(pattern...))
	return m
}
