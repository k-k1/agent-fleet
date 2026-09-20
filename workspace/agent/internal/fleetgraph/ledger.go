package fleetgraph

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// rfc3339Milli is the ledger's own timestamp format (ADR 0096 decision 2): the same
// family as Meta/instr-ledger's RFC3339, upgraded to millisecond precision because a
// write-on-change design can put two transitions in the same second.
const rfc3339Milli = "2006-01-02T15:04:05.000Z07:00"

// clockNow is the package's clock, overridable by tests that need to pin an instant (e.g.
// to land a write on a specific side of a UTC midnight boundary).
var clockNow = time.Now

// stampNow formats the current instant in the ledger's own precision.
func stampNow() string { return clockNow().Format(rfc3339Milli) }

// dir resolves fleet-graph/ under AgentStateDir, resolved on every call (never cached) so
// a test's t.Setenv("HOME", ...) takes effect — the same reason internal/fstore's base is a
// func, not a value.
func dir() string { return filepath.Join(paths.AgentStateDir(), "fleet-graph") }

func lineagePath() string { return filepath.Join(dir(), "lineage.jsonl") }

// activityPath is the day's activity file, named by its UTC date (ADR 0096 decision 2):
// a window given in millis must not have its day boundary decided by the workspace's local
// timezone, or the boundary day is silently read short.
func activityPath(day string) string { return filepath.Join(dir(), "activity-"+day+".jsonl") }

// utcDay renders t's UTC calendar date as YYYY-MM-DD.
func utcDay(t time.Time) string { return t.UTC().Format("2006-01-02") }

var (
	lineageMu  sync.Mutex
	activityMu sync.Mutex
)

// appendLine serialises v and appends it as one line under mu, creating the directory and
// file as needed. O_APPEND, one open/write/close per call, no read-modify-write (fstore's
// read-modify-write drops a concurrent writer's line — the reason this package does not use
// it, ADR 0096 decision 2 / memo fstore-no-read-modify-write).
//
// Errors are logged and swallowed: a lost graph line must never fail the request that
// triggered it (session create, an instruction delivery, a report) — the ledger is a
// secondary record, not the source of truth for any of those.
func appendLine(mu *sync.Mutex, path string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("fleetgraph: marshal %T: %v", v, err)
		return
	}
	b = append(b, '\n')
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		log.Printf("fleetgraph: mkdir %s: %v", filepath.Dir(path), err)
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("fleetgraph: open %s: %v", path, err)
		return
	}
	defer f.Close()
	if _, err := f.Write(b); err != nil {
		log.Printf("fleetgraph: write %s: %v", path, err)
	}
}

// lineageLine is the flat on-disk shape of one lineage.jsonl row: a superset of every
// LineageEvent variant's fields (ADR 0096 decision 13 / docs/log/101 §101.3), discriminated
// by Ev. Kept separate from the DTO types (dto.go) because the two use different time
// representations (decision 2) and because the DTO's shape is frozen wire contract while
// this one is free to grow a field without touching console/src/types/fleetgraph.ts.
type lineageLine struct {
	Ev            string `json:"ev"`
	Ts            string `json:"ts"`
	Name          string `json:"name"`
	Kind          string `json:"kind,omitempty"`
	Repo          string `json:"repo,omitempty"`
	Origin        string `json:"origin,omitempty"`
	OriginSession string `json:"originSession,omitempty"`
	Conv          string `json:"conv,omitempty"`
	ForkFrom      string `json:"forkFrom,omitempty"`
	Display       string `json:"display,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Code          int    `json:"code,omitempty"`
	Signal        int    `json:"signal,omitempty"`
	Archived      *bool  `json:"archived,omitempty"`
}

// activityLine is the flat on-disk shape of one activity-<day>.jsonl row: a superset of
// every ActivityEvent variant's fields. See lineageLine's doc for why it is not the DTO.
type activityLine struct {
	Ev      string `json:"ev"`
	Ts      string `json:"ts"`
	Name    string `json:"name,omitempty"` // state / resync: the lane
	From    string `json:"from,omitempty"` // instruct / report / peer / state (optional)
	To      string `json:"to,omitempty"`   // instruct / report / peer / state / resync
	Source  string `json:"source,omitempty"`
	Excerpt string `json:"excerpt,omitempty"`
	Kind    string `json:"kind,omitempty"` // report
	Reason  string `json:"reason,omitempty"`
	Intent  string `json:"intent,omitempty"` // peer
	Raw     string `json:"raw,omitempty"`    // state / resync, only when the state is unknown
}
