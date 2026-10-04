// Package sessionsearch is full-text search over past sessions' conversations, every agent kind
// alike (ADR 0110). The index is an SQLite FTS5 file under the Agent's state directory; it is a
// cache of the transcripts, which stay where each CLI keeps them, so deleting it is always safe
// and the next pass rebuilds it.
package sessionsearch

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// Seams over the session layer, so the pass can be tested without real transcripts or tmux.
var (
	listMetas     = session.ListMetas
	turnsOf       = sessionx.UsageTurns
	aliveOf       = sessionx.SessionAlive
	canTranscript = func(m session.Meta) bool { return sessionx.AgentOf(m.Kind).Caps().CanTranscript }
	indexPath     = func() string { return filepath.Join(paths.AgentStateDir(), "session-search", "index.db") }
	metaExists    = func(name string) bool { _, ok := session.ReadMeta(name); return ok }
)

// writeMu orders the pass's write of one session against Forget. The pass reads a transcript
// long after it listed the metas, and the trash may remove the meta and call Forget in between;
// without this, the pass's write would put the trashed session's text back after Forget had
// removed it. The pass re-checks the meta under the lock, and the trash removes the meta before
// it calls Forget, so either the write sees no meta or Forget runs after the write.
var writeMu sync.Mutex

// afterMetaCheck is a test seam between the meta re-check and the write, where a concurrent
// Forget would land without writeMu. Nil in production.
var afterMetaCheck func(name string)

var (
	storeMu sync.Mutex
	opened  *Store
)

// openStore returns the process's one Store, opening it on first use. A failure is not cached:
// the next call tries again.
func openStore() (*Store, error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	if opened != nil {
		return opened, nil
	}
	s, err := Open(indexPath())
	if err != nil {
		return nil, err
	}
	opened = s
	return s, nil
}

// resetStoreForTest closes and forgets the open Store.
func resetStoreForTest() {
	storeMu.Lock()
	defer storeMu.Unlock()
	if opened != nil {
		_ = opened.Close()
		opened = nil
	}
}

// The pass has no timer of its own, for the reason usage_fold.go gives: the host is shared and
// memory-constrained, and a resident sweep would re-read transcripts nobody is searching. It
// runs when a search arrives, at most once per passPeriod, in the background; the search answers
// from what is indexed and says that a pass is running.
var (
	passRunning atomic.Bool
	passGate    sync.Mutex // guards passAt only
	passAt      time.Time
	passPeriod  = time.Minute
)

// Kick starts a pass unless one ran within passPeriod (force skips that wait) and reports
// whether a pass is running when it returns.
func Kick(force bool) bool {
	if passRunning.Load() {
		return true
	}
	passGate.Lock()
	skip := !force && !passAt.IsZero() && time.Since(passAt) < passPeriod
	if !skip {
		skip = !passRunning.CompareAndSwap(false, true)
	}
	if !skip {
		passAt = time.Now()
	}
	passGate.Unlock()
	if skip {
		return passRunning.Load()
	}
	go func() {
		defer passRunning.Store(false)
		if err := runPass(); err != nil {
			log.Printf("session-search: pass: %v", err)
		}
	}()
	return true
}

// runPass brings every transcript-capable session up to date, one at a time, and drops the rows
// of sessions that no longer exist. Sessions are taken one by one so that only one transcript is
// in memory at once and a search never waits behind more than one session's write.
func runPass() error {
	s, err := openStore()
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, m := range listMetas() {
		if !canTranscript(m) {
			continue
		}
		keep[m.Name] = true
		if err := indexSession(s, m); err != nil {
			// One unreadable session must not stop the rest from being indexed.
			log.Printf("session-search: index %s: %v", m.Name, err)
		}
	}
	held, err := s.Names()
	if err != nil {
		return err
	}
	for name := range held {
		if !keep[name] {
			writeMu.Lock()
			err := s.Forget(name)
			writeMu.Unlock()
			if err != nil {
				log.Printf("session-search: forget %s: %v", name, err)
			}
		}
	}
	return nil
}

// indexSession re-reads one session's transcript unless nothing can have changed since the last
// read. A stopped session's transcript does not grow, and a resume clears its StoppedAt while the
// next stop stamps a new one, so "indexed while stopped at this same StoppedAt" proves the rows
// are current without opening the transcript — which is what keeps a pass over a long-lived
// workspace from re-reading every archived conversation.
func indexSession(s *Store, m session.Meta) error {
	settled := ""
	// An archived session cannot be running (restoring one clears StoppedAt), so the liveness
	// probe — a tmux call per session — is spent only on the listed ones.
	if m.StoppedAt != "" && (m.Archived || !aliveOf(m)) {
		settled = m.StoppedAt
	}
	cur, err := s.state(m.Name)
	if err != nil {
		return err
	}
	if settled != "" && cur.Found && cur.Settled == settled && cur.Kind == m.Kind {
		return nil
	}
	docs := DocsFromTurns(turnsOf(m))
	if len(docs) == 0 && cur.Docs > 0 && cur.Kind == m.Kind {
		// An empty read never erases what was indexed: a stopped Managed cursor session reads as
		// empty although its conversation happened. Only the trash (Forget) removes rows.
		return nil
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	if !metaExists(m.Name) {
		return nil // trashed while its transcript was being read
	}
	if afterMetaCheck != nil {
		afterMetaCheck(m.Name)
	}
	return s.Apply(m.Name, m.Kind, docs, settled)
}

// Forget removes a session's rows. The trash calls it once the meta is gone, so a deleted
// session's text is not kept in a second place until the next pass.
func Forget(name string) {
	if _, err := os.Stat(indexPath()); errors.Is(err, fs.ErrNotExist) {
		return // never searched: there is nothing to forget, and no reason to create the file
	}
	s, err := openStore()
	if err != nil {
		log.Printf("session-search: forget %s: %v", name, err)
		return
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	if err := s.Forget(name); err != nil {
		log.Printf("session-search: forget %s: %v", name, err)
	}
}
