package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// The session↔thread map of docs/37 契約6: sessionName → the Discord thread
// its notifications group into. Small JSON file under ~/.config/agent-fleet
// (home persists across container recreates). Channel is recorded so a
// destination change invalidates old mappings naturally; a hand-deleted thread
// invalidates itself via the 404 → recreate path in sendThreaded.
//
// P1.5 had a single writer (the sender goroutine). P2a adds a second reader —
// the Gateway receive loop reverse-looks-up thread→session (ThreadToSession) —
// so load/save now go through threadsMu to keep whole-file reads and writes
// from interleaving.

type threadRef struct {
	Channel string `json:"channel"`
	Thread  string `json:"thread"`
}

type threadMap map[string]threadRef

var threadsMu sync.Mutex

func threadsPath() string { return filepath.Join(paths.AgentConfigDir(), "bridge-threads.json") }

func loadThreads() threadMap {
	threadsMu.Lock()
	defer threadsMu.Unlock()
	return loadThreadsLocked()
}

func loadThreadsLocked() threadMap {
	m := threadMap{}
	b, err := os.ReadFile(threadsPath())
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

func saveThreads(m threadMap) {
	threadsMu.Lock()
	defer threadsMu.Unlock()
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(threadsPath()), 0o700)
	_ = os.WriteFile(threadsPath(), b, 0o600)
}

// ThreadToSession reverse-looks-up which session a Discord thread belongs to —
// the routing key for P2a inbound (docs/37): a reply in a session's thread is
// injected into that session. A thread MESSAGE_CREATE only carries the thread's
// own id (channel_id), not the parent channel, so we match on Thread alone. That
// is unambiguous: the forward path (sendThreaded) keeps exactly one thread per
// session and overwrites the entry when the destination channel changes, so a
// thread id in the map is always the current thread of exactly one session.
func ThreadToSession(threadID string) (string, bool) {
	threadsMu.Lock()
	defer threadsMu.Unlock()
	for name, ref := range loadThreadsLocked() {
		if ref.Thread == threadID {
			return name, true
		}
	}
	return "", false
}

// ResetThreads drops all mappings — called when the Discord connection is
// removed so a later reconnect starts clean.
func ResetThreads() {
	threadsMu.Lock()
	defer threadsMu.Unlock()
	_ = os.Remove(threadsPath())
}
