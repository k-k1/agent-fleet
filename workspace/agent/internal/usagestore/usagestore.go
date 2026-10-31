// Package usagestore keeps the last observed subscription usage of every agent kind on disk,
// so a usage chip still has something true to show after the Agent restarts (#3127).
//
// Without it a kind that holds its reading only in memory (muse's pushed quota, the copilot and
// agy caches) loses it on every restart — an idle workspace that was stopped and started again
// shows "not fetched yet" until the CLI runs once more.
//
// What is stored is each kind's own raw observation, as that kind marshals it, plus the time
// the provider really made it. Never a value worked out from it (a window rolled past its
// reset): the reader applies usagewin.Adjust at read time, so a stored reading is shown with its
// age and a passed window reads as stale, not as a fresh 0%.
//
// The account is recorded as the limitepoch epoch, never the identity itself. A reading is
// returned only while that epoch is still the current account's, so a re-login to another
// account never shows the previous one's numbers. A kind that cannot name its account ("")
// uses no store at all: two accounts that both read as "" would share one entry.
//
// Only the Agent process writes the file; the statusline subprocess keeps its own capture.
package usagestore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/limitepoch"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Entry is one kind's last observation.
type Entry struct {
	Epoch string `json:"epoch"`
	// ObservedAt is when the provider made the reading; it orders entries. RecordedAt is when
	// this Agent wrote it, for whoever reads the file by hand.
	ObservedAt time.Time       `json:"observedAt"`
	RecordedAt time.Time       `json:"recordedAt"`
	Data       json.RawMessage `json:"data"`
}

type file struct {
	V     int              `json:"v"`
	Kinds map[string]Entry `json:"kinds"`
}

const version = 1

var (
	mu       sync.Mutex
	loadedAt string // the path the in-memory copy was read from
	cur      file
)

func storePath() string { return filepath.Join(paths.AgentStateDir(), "usage-last.json") }

// load reads the file once per path. The path is part of the key because tests move HOME.
// A file of another version or one that does not parse is treated as empty: losing the last
// readings costs a "not fetched yet" chip, never a wrong number. Caller holds mu.
func load() {
	p := storePath()
	if loadedAt == p {
		return
	}
	loadedAt, cur = p, file{}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &cur)
	}
	if cur.V != version || cur.Kinds == nil {
		cur = file{V: version, Kinds: map[string]Entry{}}
	}
}

// save writes the file atomically. A failed write keeps the in-memory copy, which is still
// right for this process. Caller holds mu.
func save() {
	b, err := json.Marshal(cur)
	if err != nil {
		return
	}
	p := storePath()
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

// Record keeps data as kind's last observation, made at observedAt under the account named by
// identity. It is a no-op for a reading with no observation time, and for one not newer than
// what is already stored for the same account: two sources can deliver out of order, and an
// older reading overwriting a newer one is a chip that walks backwards. Nothing is written when
// nothing changed, so a poll that re-reads the same capture costs no disk write.
func Record(kind, identity string, observedAt time.Time, data any) {
	if observedAt.IsZero() || identity == "" {
		return
	}
	st, ok := limitepoch.Current(kind, identity, time.Now())
	if !ok || !limitepoch.Admit(st.Since, observedAt) {
		return
	}
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	load()
	if e, ok := cur.Kinds[kind]; ok && e.Epoch == st.Epoch && !observedAt.After(e.ObservedAt) {
		return
	}
	cur.Kinds[kind] = Entry{Epoch: st.Epoch, ObservedAt: observedAt.UTC(), RecordedAt: time.Now().UTC(), Data: b}
	save()
}

// Load decodes kind's stored observation into v and returns when it was observed. ok is false
// when nothing is stored, when identity is empty, when it was stored under another account than
// the one identity names now, or when it predates that account's first sighting.
func Load(kind, identity string, v any) (observedAt time.Time, ok bool) {
	if identity == "" {
		return time.Time{}, false
	}
	st, okE := limitepoch.Current(kind, identity, time.Now())
	if !okE {
		return time.Time{}, false
	}
	mu.Lock()
	load()
	e, found := cur.Kinds[kind]
	mu.Unlock()
	if !found || e.Epoch != st.Epoch || !limitepoch.Admit(st.Since, e.ObservedAt) {
		return time.Time{}, false
	}
	if json.Unmarshal(e.Data, v) != nil {
		return time.Time{}, false
	}
	return e.ObservedAt, true
}
