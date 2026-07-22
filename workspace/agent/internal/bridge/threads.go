package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// The session↔thread map of docs/37 契約6: sessionName → the Discord thread
// its notifications group into. Small JSON file under ~/.config/agent-fleet
// (home persists across container recreates). Channel is recorded so a
// destination change invalidates old mappings naturally; a hand-deleted thread
// invalidates itself via the 404 → recreate path in sendThreaded. Written only
// by the single sender goroutine, so no locking.

type threadRef struct {
	Channel string `json:"channel"`
	Thread  string `json:"thread"`
}

type threadMap map[string]threadRef

func threadsPath() string { return filepath.Join(paths.AgentConfigDir(), "bridge-threads.json") }

func loadThreads() threadMap {
	m := threadMap{}
	b, err := os.ReadFile(threadsPath())
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

func saveThreads(m threadMap) {
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(threadsPath()), 0o700)
	_ = os.WriteFile(threadsPath(), b, 0o600)
}

// ResetThreads drops all mappings — called when the Discord connection is
// removed so a later reconnect starts clean.
func ResetThreads() { _ = os.Remove(threadsPath()) }
