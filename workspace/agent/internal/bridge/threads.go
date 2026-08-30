package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// The session↔thread map of docs/log/37 契約6: sessionName → the chat thread its
// notifications group into. Small JSON file under ~/.config/agent-fleet (home
// persists across container recreates). Channel is recorded so a destination
// change invalidates old mappings naturally; a hand-deleted thread invalidates
// itself via the recreate path in the provider's threaded send.
//
// Provider-scoped (docs/log/37 Slack 追随): each provider owns its own file so Discord
// and Slack can be connected at once without colliding. Discord keeps the original
// unqualified filename (no migration of an existing map); Slack gets a suffixed one.
// `Thread` is the routing key the receive loop reverse-looks-up (thread→session):
// for Discord it is the thread's own channel id, for Slack the thread's root ts.

type threadRef struct {
	Channel string `json:"channel"`
	Thread  string `json:"thread"`
	// LastPostAt is when the bot last posted into this thread (RFC3339). The mention
	// time-gate (shouldMention) reads it to decide whether a read-only event still
	// needs the push @mention. Empty for pre-upgrade maps → treated as "quiet"
	// (mention), which is the safe default.
	LastPostAt string `json:"lastPostAt,omitempty"`
}

type threadMap map[string]threadRef

// threadStore is one provider's session↔thread map (a file + a mutex). P1.5 had a
// single writer (the sender goroutine); P2a added a reader (the receive loop's
// thread→session reverse lookup), so load/save go through mu to keep whole-file
// reads and writes from interleaving.
type threadStore struct {
	file string
	mu   sync.Mutex
}

var (
	discordThreads = &threadStore{file: "bridge-threads.json"}
	slackThreads   = &threadStore{file: "bridge-threads-slack.json"}
)

func (ts *threadStore) path() string { return filepath.Join(paths.AgentConfigDir(), ts.file) }

func (ts *threadStore) load() threadMap {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.loadLocked()
}

func (ts *threadStore) loadLocked() threadMap {
	m := threadMap{}
	b, err := os.ReadFile(ts.path())
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

func (ts *threadStore) save(m threadMap) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.writeLocked(m)
}

func (ts *threadStore) writeLocked(m threadMap) {
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(ts.path()), 0o700)
	_ = os.WriteFile(ts.path(), b, 0o600)
}

// update applies fn to the freshly-loaded map and writes it back under the lock.
// Read-modify-writes must use this rather than load()+save(): a stale snapshot
// written back would roll back a concurrent touch()'s LastPostAt (lost update →
// a spurious @mention).
func (ts *threadStore) update(fn func(threadMap)) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	m := ts.loadLocked()
	fn(m)
	ts.writeLocked(m)
}

// threadToSession reverse-looks-up which session a thread belongs to — the routing
// key for P2a inbound (docs/log/37): a reply in a session's thread is injected into that
// session. Match on Thread alone: the forward path keeps exactly one thread per
// session and overwrites the entry when the destination channel changes, so a thread
// id in the map is always the current thread of exactly one session.
func (ts *threadStore) threadToSession(threadID string) (string, bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for name, ref := range ts.loadLocked() {
		if ref.Thread == threadID {
			return name, true
		}
	}
	return "", false
}

// touch stamps a session's thread with the current time after the bot posts into it,
// feeding the mention time-gate (shouldMention). now is injected so tests are
// deterministic; production passes time.Now(). No-op if the session has no recorded
// thread (a flat/DM post has nothing to gate on).
func (ts *threadStore) touch(sessionName string, now time.Time) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	m := ts.loadLocked()
	ref, ok := m[sessionName]
	if !ok {
		return
	}
	ref.LastPostAt = now.UTC().Format(time.RFC3339)
	m[sessionName] = ref
	ts.writeLocked(m)
}

// reset drops all mappings — called when the connection is removed so a later
// reconnect starts clean.
func (ts *threadStore) reset() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	_ = os.Remove(ts.path())
}

// --- Discord free-function wrappers (zero-change for the Discord send/receive path) ---

func loadThreads() threadMap                            { return discordThreads.load() }
func saveThreads(m threadMap)                           { discordThreads.save(m) }
func updateThreads(fn func(threadMap))                  { discordThreads.update(fn) }
func touchThreadPost(sessionName string, now time.Time) { discordThreads.touch(sessionName, now) }

// ThreadToSession is the Discord receive loop's reverse lookup (exported for
// receiver.go). Slack's receive loop uses slackThreads.threadToSession directly.
func ThreadToSession(threadID string) (string, bool) { return discordThreads.threadToSession(threadID) }

// ResetThreads drops the Discord mappings on disconnect.
func ResetThreads() { discordThreads.reset() }

// ResetSlackThreads drops the Slack mappings on disconnect.
func ResetSlackThreads() { slackThreads.reset() }
