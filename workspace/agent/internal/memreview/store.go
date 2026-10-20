package memreview

// The review's durable state (ADR 0117 decisions 1, 3 and 8): one record per claimed run, one
// watermark per session, the daily call counter. All of it lives in one directory on the same
// mount as the memory store, outside the memory repo, so nothing a review wrote is ever part of the
// published history and a "Recreate" of the working copies does not touch it.
//
//	runs/<run id>.json        the claim and everything needed to resume it
//	watermark/<key hash>.json how far a session has been reviewed
//	daily.json                the calls made today
//	pending/, rejected/       the proposal queue (queue.go)
//
// Writes are durable (durable.go): synced before they are relied on, so a record written before a
// call is still there after the host fails, and a reader or a restart sees the old record or the
// new one, never half of one.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// The bounds of ADR 0117 decision 3, all confirmed by the owner (the daily cap and the pending
// bound remain policy assumptions to be revisited from P1 traffic, #2333).
const (
	// MaxAttempts is one call and at most one retry for a window.
	MaxAttempts = 2
	// DailyCallCap is the helper calls per workspace per local day. A bound against a runaway
	// loop sized from one heavy-use sample (P0), not a budget; the budgets UI is P3's.
	DailyCallCap = 250
	// MaxPending bounds the proposal queue: a review that nobody reads must not grow it forever.
	// Confirmed by the owner on 2026-10-18 (the P0 journal gave only "queued sessions: 20", a
	// policy assumption, so this number was put to them); revisited from P1 traffic (#2333).
	MaxPending = 100
	// DefaultBackoff is the wait after a transient failure before the same run is tried again.
	DefaultBackoff = 5 * time.Minute

	keepTerminalRuns = 200
)

// Run states. A run is created "claimed" with its window fixed, and ends "done" or "failed".
const (
	StateClaimed    = "claimed"    // window fixed; no call in flight
	StateDispatched = "dispatched" // an attempt was recorded and the call may have been made
	StateAnswered   = "answered"   // a validated reply is saved in Ops; queueing is what is left
	StateDone       = "done"
	StateFailed     = "failed"
)

// Attempt outcomes. Failure texts are a closed vocabulary: nothing the model or a process printed is
// stored in a run record.
const (
	OutcomeDispatched = "dispatched"  // written before the call; it is also what a crash leaves
	OutcomeAnswered   = "answered"    // a reply that passed validation
	OutcomeFailed     = "call-failed" // the call returned an error
	OutcomeMalformed  = "malformed"   // the reply was dropped whole (shape, size, evidence...)
	OutcomeSecret     = "secret"      // the reply echoed something the scan refuses
	OutcomeLost       = "lost"        // dispatched, and the process ended before the answer was kept
)

// Attempt is one helper call. It is written before the call, so a crash leaves a trace.
type Attempt struct {
	N         int       `json:"n"`
	StartedAt time.Time `json:"startedAt"`
	Outcome   string    `json:"outcome"`
	// EndedAt is when the attempt got its outcome (zero for a lost one, which never did).
	EndedAt time.Time `json:"endedAt,omitempty"`
	// Cost says what is known of a lost attempt's cost: "linked" when a ledger row for it was
	// found (Spend is its tokens), "unknown" when none was (the call may or may not have been
	// billed). Empty for an attempt that ended normally: the ledger has its row.
	Cost  string `json:"cost,omitempty"`
	Spend int    `json:"spend,omitempty"`
	// Call is the ledger call id the attempt was linked to; no other attempt is linked to it.
	Call string `json:"call,omitempty"`
}

// Run is the durable record of one review of one window.
type Run struct {
	ID      string `json:"id"`
	Key     string `json:"key"`     // the session's identity (the watermark's key)
	Session string `json:"session"` // its name: store lookup and the ledger's ref
	Trigger string `json:"trigger"` // auto | manual
	// Base is the watermark when the run was claimed; First and Last are the turn numbers the
	// window shows. Last is the window end the run is keyed by and the watermark moves to.
	Base     int       `json:"base"`
	First    int       `json:"first"`
	Last     int       `json:"last"`
	State    string    `json:"state"`
	Attempts []Attempt `json:"attempts,omitempty"`
	// Ops are the validated reply, kept only while queueing is unfinished: a run that has ended
	// carries no proposal text (the queue owns it, and deletes it on a decision).
	Ops       []Op   `json:"ops,omitempty"`
	ProjectID string `json:"projectId,omitempty"`
	// Queued and Skipped are what queueing did, as counts.
	Queued  int `json:"queued,omitempty"`
	Skipped int `json:"skipped,omitempty"`
	// NotifyPending is set in the write that ends a run which left proposals in the queue and
	// cleared once the Console notification for it has been made durable (OnQueued). A stop between
	// the two leaves it set, and ReconcileNotices finishes the notification.
	NotifyPending bool   `json:"notifyPending,omitempty"`
	Failure       string `json:"failure,omitempty"`
	// Unsent counts the times no backend could run the review without tools; those calls never
	// reached a model, so they are a counter and not attempts.
	Unsent    int       `json:"unsent,omitempty"`
	NotBefore time.Time `json:"notBefore,omitempty"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
}

// Terminal reports whether nothing more happens to the run.
func (r Run) Terminal() bool { return r.State == StateDone || r.State == StateFailed }

// used counts the attempts that spent budget: each reached, or may have reached, a model.
func (r Run) used() int { return len(r.Attempts) }

// runID is keyed by the session identity and the fixed window end, so two triggers cannot claim
// the same window and a restart finds the same run.
func runID(key string, last int) string {
	h := sha256.Sum256([]byte(key + "\x00" + strconv.Itoa(last)))
	return hex.EncodeToString(h[:])[:20]
}

func keyHash(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])[:24]
}

// Errors the caller handles: all of them leave the run, the watermark and the queue as they were.
var (
	ErrBusy      = errors.New("another review is in flight")
	ErrBackoff   = errors.New("the review is backing off after a failure")
	ErrDailyCap  = errors.New("the daily review call cap is spent")
	ErrQueueFull = errors.New("the proposal queue is full")
	ErrNoBackend = errors.New("no backend that can run the review without tools is available")
	ErrDisabled  = errors.New("the memory review is switched off")
)

// stateRoot is the review's directory. It is resolved on every call: tests move HOME and
// CLAUDE_CONFIG_DIR.
func stateRoot() string { return filepath.Join(paths.ClaudeConfigDir(), "af-memory-review") }

var (
	runStore       = store[Run]{"runs"}
	watermarkStore = store[watermark]{"watermark"}
	dailyStore     = store[daily]{""}

	// stateMu orders every read-modify-write of the files above inside this process. The Agent is
	// their only writer.
	stateMu sync.Mutex
)

type watermark struct {
	Key  string    `json:"key"`
	Turn int       `json:"turn"` // the last turn number that has been reviewed
	At   time.Time `json:"at"`
}

type daily struct {
	Day   string `json:"day"` // the local date
	Calls int    `json:"calls"`
}

// Watermark is the last reviewed turn number of a session (0: none).
func Watermark(key string) int {
	stateMu.Lock()
	defer stateMu.Unlock()
	w, _ := watermarkStore.Read(keyHash(key))
	return w.Turn
}

// moveWatermark only ever moves it forward.
func moveWatermark(key string, turn int, now time.Time) error {
	stateMu.Lock()
	defer stateMu.Unlock()
	w, _ := watermarkStore.Read(keyHash(key))
	if turn <= w.Turn {
		return nil
	}
	return watermarkStore.Write(keyHash(key), watermark{Key: key, Turn: turn, At: now})
}

// saveRun persists a run record.
func saveRun(r *Run, now time.Time) error {
	stateMu.Lock()
	defer stateMu.Unlock()
	r.Updated = now
	return runStore.Write(r.ID, *r)
}

// runs lists every run record, newest first.
func runs() []Run {
	stateMu.Lock()
	defer stateMu.Unlock()
	return listRunsLocked()
}

func listRunsLocked() []Run {
	var out []Run
	for _, id := range runStore.keys() {
		if r, ok := runStore.Read(id); ok {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

// unfinishedRun is the session's run that has not ended, if any. There is at most one: a window
// is claimed only when none is open.
func unfinishedRun(key string) (Run, bool) {
	for _, r := range runs() {
		if r.Key == key && !r.Terminal() {
			return r, true
		}
	}
	return Run{}, false
}

// pruneRuns keeps the newest terminal records, drops decided markers nothing can replay any more
// and sweeps staged files a killed writer left behind. An open run is never removed.
func pruneRuns(now time.Time) {
	stateMu.Lock()
	defer stateMu.Unlock()
	kept := 0
	for _, r := range listRunsLocked() {
		if !r.Terminal() {
			continue
		}
		// A run whose notification is still owed is the only record of it (ReconcileNotices reads
		// it), so it stays while one of its own proposals is waiting. That bounds the exception:
		// the queue holds at most MaxPending proposals, and a mark whose proposals were all decided
		// no longer protects its run.
		if r.NotifyPending && runHasPendingLocked(r.ID) {
			continue
		}
		if kept++; kept <= keepTerminalRuns {
			continue
		}
		// An ended run is the evidence repairWatermark works from until the session's watermark has
		// reached its window: pruning it first would let a stop between the ending and the watermark
		// make the window reviewable again.
		if w, _ := watermarkStore.Read(keyHash(r.Key)); w.Turn >= r.Last {
			_ = runStore.Remove(r.ID)
		}
	}
	gcDecidedLocked()
	for _, s := range []interface {
		sweepStaging(time.Duration, time.Time)
	}{runStore, watermarkStore, dailyStore, pendingStore, rejectedStore, decidedStore, releaseStore} {
		s.sweepStaging(time.Hour, now)
	}
}

// repairWatermark moves the watermark to the end of the newest ended run: a run is ended before
// the watermark moves, so a stop between the two must not make the window reviewable again.
func repairWatermark(key string, now time.Time) error {
	last := 0
	for _, r := range runs() {
		if r.Key == key && r.Terminal() && r.Last > last {
			last = r.Last
		}
	}
	if last == 0 {
		return nil
	}
	return moveWatermark(key, last, now)
}

// consumeCall counts one helper call against today's cap and persists the count before the call
// is made: a crash then over-counts by one instead of letting a loop under-count forever.
func consumeCall(now time.Time) error {
	stateMu.Lock()
	defer stateMu.Unlock()
	d, _ := dailyStore.Read("daily")
	day := now.Format("2006-01-02")
	if d.Day != day {
		d = daily{Day: day}
	}
	if d.Calls >= DailyCallCap {
		return ErrDailyCap
	}
	d.Calls++
	return dailyStore.Write("daily", d)
}

// refundCall takes back a call that never reached a model.
func refundCall(now time.Time) {
	stateMu.Lock()
	defer stateMu.Unlock()
	d, _ := dailyStore.Read("daily")
	if d.Day == now.Format("2006-01-02") && d.Calls > 0 {
		d.Calls--
		_ = dailyStore.Write("daily", d)
	}
}

// CallsToday is today's count, for display and tests.
func CallsToday(now time.Time) int {
	stateMu.Lock()
	defer stateMu.Unlock()
	d, _ := dailyStore.Read("daily")
	if d.Day != now.Format("2006-01-02") {
		return 0
	}
	return d.Calls
}
