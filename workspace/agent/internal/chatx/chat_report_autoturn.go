package chatx

// Debounce that bundles the automatic turns triggered by completion reports.
//
// The reconciler's delivery (deliverReportCard) appends a report card to the conversation
// immediately, but the operator's automatic turn is not run once per report: reports are
// bundled over a short window and one turn runs at the end of it. An automatic turn is an
// expensive call that makes the provider re-read the conversation's whole context (system
// prompt, tool schemas, history), and runReportAutoTurn already puts every undelivered
// report on a single turn (undeliveredReports) — the bundling machinery existed, the only
// missing piece was waiting a little. In the typical case of several sessions finishing
// close together (parallel instructions converging, the same sweep tick), the number of
// turns, and so of context re-reads, drops from the number of reports to the number of
// windows.
//
// Only the operator's follow-up turn is delayed: the report card itself reaches the
// conversation and the notification center at once, so the completion the user sees is not
// late. If the user speaks during the window the reports ride along on that turn
// (injectPendingReports), and the timer that fires afterwards sees nothing undelivered and
// becomes a no-op.
//
// The immediate turns for interim reports (question / plan-approval) do not come through
// here (chat_report.go deliverSessionReport): on that path latency is the user's experience
// of answering a question, so it is never bundled (docs/log/30).

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
	"os"
	"strconv"
	"sync"
	"time"
)

// ChatAutoTurnDelayDefault is the bundling window. The reconciler's settle debounces over
// two 15s ticks, so completions of parallel sessions arrive spread over tens of seconds —
// the window has to be long enough to fold that into one turn. Overridable through settings
// (Settings > Assistant, auto-reply bundling window; ui-prefs assistantAutoTurnDelay, in
// seconds) or AF_CHAT_AUTOTURN_DELAY (seconds); 0 means immediate.
const ChatAutoTurnDelayDefault = 60 * time.Second

// ChatAutoTurnDelayMax caps the configurable window: beyond this, bundling gains nothing
// more and only the follow-up to a report gets slower.
const ChatAutoTurnDelayMax = 10 * time.Minute

// ChatAutoTurnDelay returns the effective bundling window (settings, then env, then default).
func ChatAutoTurnDelay() time.Duration {
	if v, ok := uiprefs.Read()["assistantAutoTurnDelay"].(float64); ok && v >= 0 {
		d := time.Duration(v) * time.Second
		if d > ChatAutoTurnDelayMax {
			return ChatAutoTurnDelayMax
		}
		return d
	}
	if v := os.Getenv("AF_CHAT_AUTOTURN_DELAY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	return ChatAutoTurnDelayDefault
}

// autoTurnScheduler runs one deferred turn per conversation per window.
type autoTurnScheduler struct {
	delay func() time.Duration
	run   func(convID string)

	mu      sync.Mutex
	pending map[string]*time.Timer
}

func newAutoTurnScheduler(delay func() time.Duration, run func(convID string)) *autoTurnScheduler {
	return &autoTurnScheduler{delay: delay, run: run, pending: map[string]*time.Timer{}}
}

// reportAutoTurns is the process-wide scheduler. A crash loses the open window, but not the
// reports: injectPendingReports picks up anything undelivered when the next turn is
// submitted, the same degradation as losing an in-flight go runReportAutoTurn.
var reportAutoTurns = newAutoTurnScheduler(ChatAutoTurnDelay, runReportAutoTurn)

// schedule requests one operator turn for the conversation after the bundling
// window. The first report opens the window and later ones ride the same firing; the timer
// is deliberately not reset on each arrival, because a resetting window starves the turn for
// as long as reports keep arriving faster than the window. A fixed window guarantees the
// delay is at most the window length.
func (s *autoTurnScheduler) schedule(convID string) {
	d := s.delay()
	if d <= 0 {
		go s.run(convID)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pending[convID]; ok {
		return
	}
	s.pending[convID] = time.AfterFunc(d, func() {
		s.mu.Lock()
		delete(s.pending, convID)
		s.mu.Unlock()
		s.run(convID)
	})
}
