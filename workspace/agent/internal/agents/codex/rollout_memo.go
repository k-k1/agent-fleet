package codex

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// rolloutPath finds a session's rollout by globbing sessions/*/*/*/rollout-*<id>.jsonl, and
// Go's Glob answers that with one directory read per date level entry: (3 + number of day
// directories) opens per call. ~/.codex lives on a network mount, and the notification outbox
// drain (GET /notifications → PendingQuestionID) calls through here for every codex session
// every few seconds. Measured on one production box: 34 sessions × (12 directory opens + 1
// rollout open) = 442 opens per drain.
//
// The id → rollout mapping is stable (codex writes a session's rollout into the date directory
// of its start and leaves it), so a found path is remembered and confirmed with one Lstat.
//
// Invariants:
//   - A MISS IS NEVER REMEMBERED. Callers ask "is there a rollout yet" (fork source, fork-at,
//     delivery checks); a remembered absence would hide a rollout that appeared a moment later.
//     remember() refuses to store "" and remembered() refuses to serve it; both are kept on
//     purpose, as in claude/jsonl_memo.go.
//   - A remembered path that still Lstats is served until rolloutMemoTTL: a refresh costs a full
//     sweep of the network mount per id, so it runs at most once per id per TTL, and the Lstat
//     already catches a rollout that moved or was deleted.
//   - TTL is the ONLY thing that picks up a newer duplicate for the same id in the resident
//     Agent. The session-status hook (RememberSid) runs in a short-lived `workspace-agent
//     session-status` process, so its forget() clears that process's empty memo, never the
//     Agent's. The managed driver's RememberSid call passes a new thread id (start / fork),
//     which matches no remembered key. A managed resume of a known thread, the Terminal route's
//     `codex resume` / `codex fork`, and a thread resumed by another app-server client call no
//     forget at all. The repo's measurements say resume appends to the existing rollout and a
//     fork gets a new id (a new key), but nothing rules out a codex version writing a second file
//     for the same id. Never drop the TTL on the belief that forget covers it; a late duplicate
//     is bounded by the hour.
//
// The key carries the sessions root: HOME moves in tests and between users.
type rolloutMemo struct {
	mu   sync.Mutex
	seen map[string]rolloutHit
}

type rolloutHit struct {
	path string
	at   time.Time
}

// forget drops every remembered path for codexID, whatever its sessions root.
func (m *rolloutMemo) forget(codexID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.seen {
		if strings.HasSuffix(k, "\x00"+codexID) {
			delete(m.seen, k)
		}
	}
}

// Like claude's memos, both tables below grow by one small entry per session the agent ever
// saw; one process per workspace holds tens of sessions, so they are not worth evicting.
const rolloutMemoTTL = time.Hour

var rolloutPaths rolloutMemo

// rolloutGlob is the search seam; tests count its calls.
var rolloutGlob = filepath.Glob

func (m *rolloutMemo) lookup(key string, search func() string) string {
	if p, ok := m.remembered(key); ok {
		return p
	}
	p := search()
	m.remember(key, p)
	return p
}

func (m *rolloutMemo) remembered(key string) (string, bool) {
	m.mu.Lock()
	hit, ok := m.seen[key]
	m.mu.Unlock()
	if !ok || hit.path == "" || time.Since(hit.at) > rolloutMemoTTL {
		return "", false
	}
	if _, err := os.Lstat(hit.path); err != nil {
		return "", false
	}
	return hit.path, true
}

func (m *rolloutMemo) remember(key, path string) {
	if path == "" {
		return // a miss is never remembered
	}
	m.mu.Lock()
	if m.seen == nil {
		m.seen = map[string]rolloutHit{}
	}
	m.seen[key] = rolloutHit{path: path, at: time.Now()}
	m.mu.Unlock()
}

// questionScan is the file-derived half of PendingQuestionID: the request_user_input calls
// still open at the end of the rollout's tail, valid while the file's size and mtime hold.
// Only this half is cached. The route-dependent half (the pane's start time) is applied on
// every call, so no input other than the file can make the cached part stale. The design errs
// toward nothing: an append changes the size, so a new question or its answer is seen on the
// next call; the one blind spot is a same-size, same-mtime rewrite, which an append-only
// rollout does not do.
type questionScan struct {
	size    int64
	mtime   time.Time
	askedAt map[string]string
}

var (
	questionScansMu sync.Mutex
	questionScans   = map[string]questionScan{}
)
