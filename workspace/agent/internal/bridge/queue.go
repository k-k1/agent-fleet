package bridge

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// maxQueue bounds the on-disk delivery queue (docs/log/37 「不達と滞留」: 溢れたら
// 古い方から破棄). The queue only grows while the daemon is down or every send
// is failing, so the bound is a backstop, not a working size.
const maxQueue = 200

// maxAttempts is the bounded-retry limit per queued message; beyond it the
// message is dropped with a log line (fire-and-forget, docs/log/37 契約4).
const maxAttempts = 5

func queueDir() string { return filepath.Join(paths.AgentConfigDir(), "bridge-queue") }

// queued is the on-disk envelope: the message plus its delivery attempt count
// (persisted so retries survive a daemon restart). Delivered tracks, per provider
// name, how many of the message's sub-messages already landed, so a retry resumes
// instead of re-posting from scratch (docs/log/37 重複対策 — see ResumableSender).
type queued struct {
	Message
	Attempts  int            `json:"attempts,omitempty"`
	Delivered map[string]int `json:"delivered,omitempty"`
}

// Enqueue drops a message into the delivery queue. Safe from ANY process
// (daemon or dying hook shell): one small file write, no locking, no network.
// Errors are swallowed — bridge delivery must never affect the caller (the
// notification outbox especially). Kinds outside the bridged set are ignored
// here so unconfigured deployments accumulate nothing they'd never send.
func Enqueue(m Message) {
	if eventKeyFor(m.Kind) == "" {
		return
	}
	if m.CreatedAt == "" {
		m.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	dir := queueDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	b, err := json.Marshal(queued{Message: m})
	if err != nil {
		return
	}
	// Zero-padded nanos keep lexicographic order == arrival order; the random
	// suffix disambiguates concurrent writers (hook subprocesses race).
	suf := make([]byte, 4)
	_, _ = rand.Read(suf)
	name := fmt.Sprintf("%020d-%s.json", time.Now().UnixNano(), hex.EncodeToString(suf))
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
		return
	}
	pruneQueue(dir)
}

// pruneQueue enforces maxQueue by dropping the oldest entries.
func pruneQueue(dir string) {
	names := queueFiles(dir)
	for i := 0; i <= len(names)-1-maxQueue; i++ {
		_ = os.Remove(filepath.Join(dir, names[i]))
	}
}

// queueFiles lists the queue entries oldest-first.
func queueFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, ent := range entries {
		if !ent.IsDir() && strings.HasSuffix(ent.Name(), ".json") {
			names = append(names, ent.Name())
		}
	}
	sort.Strings(names)
	return names
}

// readQueued loads one queue entry; a corrupt file is removed and skipped.
func readQueued(path string) (queued, bool) {
	var q queued
	b, err := os.ReadFile(path)
	if err != nil {
		return q, false
	}
	if err := json.Unmarshal(b, &q); err != nil {
		log.Printf("bridge: drop corrupt queue entry %s: %v", filepath.Base(path), err)
		_ = os.Remove(path)
		return q, false
	}
	return q, true
}
