package bridge

import (
	"crypto/rand"
	"crypto/sha256"
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

// maxQueue bounds the on-disk delivery queue (docs/log/37 §non-delivery and backlog:
// on overflow the oldest go first). The queue only grows while the daemon is down or
// every send is failing, so the bound is a backstop, not a working size.
const maxQueue = 200

// maxAttempts is the bounded-retry limit per queued message; beyond it the
// message is dropped with a log line (fire-and-forget, docs/log/37 contract 4).
const maxAttempts = 5

func queueDir() string { return filepath.Join(paths.AgentStateDir(), "bridge-queue") }

// queued is the on-disk envelope: the message plus its delivery attempt count
// (persisted so retries survive a daemon restart). Delivered tracks, per provider
// name, how many of the message's sub-messages already landed, so a retry resumes
// instead of re-posting from scratch (docs/log/37 de-duplication — see ResumableSender).
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
	m.Target = ""
	enqueue(m)
}

// EnqueueToOnce queues m for the provider named target alone, whatever its event toggles say
// (the member asked for this message on that connection specifically), and at most once per key,
// across restarts: a caller that retries after a crash (its own state not yet updated) finds the
// marker and queues nothing. The caller checks TargetReady first, so a connection that is gone or
// unbound is reported instead of silently skipped. The marker is
// written right after the queue entry, so an error leaves no marker and the caller may retry; the
// only window left is a crash between those two local writes.
func EnqueueToOnce(key, target string, m Message) error {
	if target == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(key))
	dir := sentDir()
	marker := filepath.Join(dir, hex.EncodeToString(sum[:])+".sent")
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	m.Target = target
	if err := enqueueErr(m); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	pruneSent(dir)
	return os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)), 0o600)
}

// sentDir holds EnqueueToOnce's markers.
func sentDir() string { return filepath.Join(paths.AgentStateDir(), "bridge-sent") }

// sentKeep is how long a marker is kept: far past any retry of the row it guards, which is
// settled within minutes, or reported again only after an hours-long outage.
const sentKeep = 30 * 24 * time.Hour

// pruneSent drops markers past sentKeep. Called on a write, which is rare (one per scheduled
// result per connection), so the directory listing costs nothing that matters.
func pruneSent(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-sentKeep)
	for _, e := range ents {
		if info, err := e.Info(); err == nil && !e.IsDir() && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func enqueue(m Message) { _ = enqueueErr(m) }

// enqueueErr is enqueue reporting whether the entry was written.
func enqueueErr(m Message) error {
	if m.CreatedAt == "" {
		m.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	dir := queueDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(queued{Message: m})
	if err != nil {
		return err
	}
	// Zero-padded nanos keep lexicographic order == arrival order; the random
	// suffix disambiguates concurrent writers (hook subprocesses race).
	suf := make([]byte, 4)
	_, _ = rand.Read(suf)
	name := fmt.Sprintf("%020d-%s.json", time.Now().UnixNano(), hex.EncodeToString(suf))
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
		return err
	}
	pruneQueue(dir)
	return nil
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
