package memconsol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Observation sources.
const (
	SourceFeedback = "feedback"      // a stale / wrong reason from memory_feedback
	SourceLint     = "lint"          // a lint reason
	SourceExport   = "review-export" // a proposal ADR 0117's review left unanswered
)

// Candidate is the memory an exported 0117 create proposal carried. It is evidence, never an
// approval: it becomes a queued create only after the engine classified the observation as an
// add and every deterministic check passed, and publishes only on the member's approval.
type Candidate struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Body        string `json:"body"`
}

// Observation is one thing someone noticed about a memory.
type Observation struct {
	ID        string     `json:"id"`
	Source    string     `json:"source"`
	Scope     string     `json:"scope"`
	ProjectID string     `json:"projectId,omitempty"`
	Memory    string     `json:"memory"` // the suspect memory (the proposed name for an exported create)
	Rule      string     `json:"rule"`
	Note      string     `json:"note"`
	Revision  int        `json:"revision,omitempty"`
	FirstSeen time.Time  `json:"firstSeen"`
	Candidate *Candidate `json:"candidate,omitempty"`
}

// IsReport says whether the observation is a wrong or stale report from a reader: the strongest
// signal, ripe on its own (decision 6).
func (o Observation) IsReport() bool {
	return o.Source == SourceFeedback && (o.Rule == "stale" || o.Rule == "wrong")
}

func obsID(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:20]
}

// Coverage records that an observation has been looked at: the proposal it ended in ("" when the
// engine said no-op) and the revision its memory had then. A covered observation does not count
// towards ripeness. It counts again only when its memory has changed since (decision 7: "a change
// to the target memory"); expiry or a rejection alone never reopens it.
type Coverage struct {
	Proposal string    `json:"proposal,omitempty"`
	Revision int       `json:"revision"`
	At       time.Time `json:"at"`
	Verdict  string    `json:"verdict,omitempty"`
}

// ObsState is what the consolidator remembers about one observation id.
type ObsState struct {
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	Covered   *Coverage `json:"covered,omitempty"`
	// Dropped counts engine answers about it that failed validation; at maxDropped it is left
	// alone until the evidence changes (a changed note is a different id).
	Dropped int `json:"dropped,omitempty"`
}

const maxDropped = 2

// TriggerState is what the trigger keeps across restarts.
type TriggerState struct {
	LastRun    time.Time `json:"lastRun,omitempty"`
	LastTick   time.Time `json:"lastTick,omitempty"`
	LastNotice time.Time `json:"lastNotice,omitempty"`
	// BusyTicks counts ticks since the last run on which a session was active; a ripe topic that
	// waits while this grows is delayed by "never-idle".
	BusyTicks int `json:"busyTicks,omitempty"`
	// StoppedGap is set when a tick found the previous one far in the past (the workspace was
	// stopped or the Agent was down); cleared by the next run.
	StoppedGap bool `json:"stoppedGap,omitempty"`
}

// State is the consolidator's own bookkeeping, one file under Dir. It never holds memory text.
type State struct {
	Version      int                  `json:"version"`
	Observations map[string]*ObsState `json:"observations"`
	Trigger      TriggerState         `json:"trigger"`
}

const stateVersion = 1

var stateMu sync.Mutex // one writer of state.json; also guards the queue's read-modify-write

func statePath() string { return filepath.Join(Dir(), "state.json") }

func loadState() (*State, error) {
	s := &State{Version: stateVersion, Observations: map[string]*ObsState{}}
	b, err := os.ReadFile(statePath())
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	if s.Observations == nil {
		s.Observations = map[string]*ObsState{}
	}
	return s, nil
}

func (s *State) save() error { return writeJSONAtomic(statePath(), s) }

// writeJSONAtomic writes v as JSON to path through a temp file, fsync and rename, so a stop never
// leaves half a file.
func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
