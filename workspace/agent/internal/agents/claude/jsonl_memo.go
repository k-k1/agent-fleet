package claude

import (
	"os"
	"sync"
	"time"
)

// Finding a session's transcript means finding WHICH projects/<project>/ directory holds
// <sid>.jsonl, and the obvious way to ask — filepath.Glob("projects/*/<sid>.jsonl") — answers
// it by reading every project directory there is. Go's Glob expands the `*` component with one
// ReadDir of projects/ and then one ReadDir per match, so the cost is (1 + number of projects)
// directory reads per call, not one stat.
//
// That would be unremarkable on a local disk. CLAUDE_CONFIG_DIR is an EFS (NFS) mount, so each
// of those reads is a network round trip, and every transcript-shaped feature calls through
// here — the mirror, usage, context fill, abort detection, background-work detection, the
// Remote Control URL — once per session per poll.
//
// Measured on a production workspace with 38 project directories and NOT ONE claude session
// running: 821 directory reads per second from the agent alone. Across the deployment that
// exhausted the file system's burst credits, collapsing it to its ~124 KiB/s baseline, and
// every workspace stopped being usable — opening a 2.4 KB settings file took 5.3 seconds.
//
// The mapping is stable: claude writes a sid's transcript into one project directory and
// leaves it there. So remember where it was found and, next time, confirm that one path
// instead of searching for it again — (1 + N) directory reads become one stat.
//
// Two invariants hold the correctness:
//
//   - A MISS IS NEVER REMEMBERED. Answering "no transcript" when one exists makes buildProgram
//     pass --session-id instead of --resume, and claude exits with "Session ID is already in
//     use". A remembered hit is re-checked on disk, so the only way to serve a wrong answer is
//     to invent an absence — never do it. Two places enforce this, deliberately: remember()
//     refuses to store an empty result and remembered() refuses to serve one. Deleting either
//     alone leaves the behaviour correct and the tests green, which is precisely why both are
//     written down here — the next reader will otherwise take one out as dead code.
//   - A hit is re-searched after memoTTL anyway, so a transcript that genuinely moved is picked
//     up without anyone having to invalidate by hand.
//
// ⚠️ bg.go has an absenceMemo that does the opposite — it remembers "this session has no
// background agents" for 15 seconds. That is not a copy of this type with the invariant
// dropped, and neither belongs on the other's path: the absence there is read by ONE caller,
// which lights a badge, while the absence here would be read by the resume decision. The
// argument for each is written at both ends on purpose, because the two look like duplicates
// and the natural tidy-up is to unify them.
type pathMemo struct {
	mu   sync.Mutex
	seen map[string]memoHit
}

type memoHit struct {
	paths []string
	at    time.Time
}

// memoTTL bounds how long a remembered answer is trusted before the search runs again. It
// only has to be short relative to "a transcript moved", which needs a session restart into a
// different directory; the poll loops it protects run every few seconds.
//
// It stays at 60s now that the project directory is derived from the session's cwd
// (project_dir.go): the re-search this TTL forces used to mean a full `projects/*` sweep
// every minute per session, and now means one more Lstat whenever the cwd is known. There is
// nothing left to buy by lengthening it, and shortening it buys freshness nobody has asked
// for (ADR 0087 decision 5, third bullet).
const memoTTL = 60 * time.Second

// lookup returns the memoized paths for key, falling back to search. The key must include
// ConfigDir(): it moves under CLAUDE_CONFIG_DIR, and tests point it at a fresh temp dir.
func (m *pathMemo) lookup(key string, search func() []string) []string {
	if paths, ok := m.remembered(key); ok {
		return paths
	}
	paths := search()
	m.remember(key, paths)
	return paths
}

// remembered returns the memoized answer only while it is still true on disk. A single Lstat
// per remembered path replaces the directory sweep; anything unexpected (gone, renamed,
// unreadable) falls through to the search rather than guessing.
func (m *pathMemo) remembered(key string) ([]string, bool) {
	m.mu.Lock()
	hit, ok := m.seen[key]
	m.mu.Unlock()
	if !ok || len(hit.paths) == 0 || time.Since(hit.at) > memoTTL {
		return nil, false
	}
	for _, p := range hit.paths {
		if _, err := os.Lstat(p); err != nil {
			return nil, false
		}
	}
	return hit.paths, true
}

func (m *pathMemo) remember(key string, paths []string) {
	if len(paths) == 0 {
		return // see the invariant in the type comment: a miss is never remembered
	}
	m.mu.Lock()
	if m.seen == nil {
		m.seen = map[string]memoHit{}
	}
	m.seen[key] = memoHit{paths: paths, at: time.Now()}
	m.mu.Unlock()
}

// The agent is one process per workspace and a workspace holds tens of sessions, so these grow
// to tens of entries and are not worth evicting.
var (
	jsonlMemo    pathMemo
	subagentMemo pathMemo
)
