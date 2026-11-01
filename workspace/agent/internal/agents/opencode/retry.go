package opencode

// A provider error under retry.
//
// Measured (1.19.0, loopback stub provider answering every request with one status):
//
//	400 401 402 403 404 413   fail at once, nothing is retried (the assistant message carries the error)
//	429 (rate limit AND insufficient_quota), 500, 503, 529
//	                           retried without end: GET /session/status keeps answering
//	                           {"type":"retry","attempt":N,"message":…,"next":<epoch ms>} with the
//	                           backoff growing to ~17s by attempt 4, and /message never returns
//
// So a retry status is the only place a permanent condition that rides on a retryable status shows
// up (an exhausted quota on a 429, a region or privacy refusal a gateway wraps in a 500). The same
// status is the session.retry.scheduled event on opencode 2 (opencode 1 sent session.status, which
// the driver still accepts); 2.x gives up on its own after ~11 attempts, but a permanent message there still burns that minute and a half.
// Aborting a session that is retrying leaves no error on the assistant message (measured: error
// null, parts empty), so the failure has to be carried by the driver (threadHandle.retryStop).

import (
	"fmt"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// permanentRetryPhrases are lower-cased fragments of upstream messages that no retry can fix.
// Only well-defined phrases belong here; when in doubt a message is transient, because ending a
// turn that would have recovered is worse than waiting for one that never will.
var permanentRetryPhrases = []string{
	// region / privacy settings the member has to change
	"requires global regions",
	"not available in your region",
	"unsupported_country_region_territory",
	// quota and billing
	"exceeded your current quota",
	"insufficient_quota",
	"insufficient balance",
	"insufficient credits",
	"credit balance is too low",
	// credentials
	"incorrect api key",
	"invalid api key",
	"invalid_api_key",
	"invalid x-api-key",
}

// permanentRetry reports whether a retry message names a condition that retrying cannot fix.
func permanentRetry(msg string) bool {
	m := strings.ToLower(msg)
	for _, p := range permanentRetryPhrases {
		if strings.Contains(m, p) {
			return true
		}
	}
	return false
}

// permanentRetryMinAttempt is the attempt from which a permanent-looking message ends the turn:
// seeing it twice keeps one odd response from a flaky gateway from failing a turn that recovers.
const permanentRetryMinAttempt = 2

// retryFailure is the turn's failure text for a message that ended it, tagged like errors.go's.
func retryFailure(msg string) string {
	return fmt.Sprintf("[error] provider error opencode kept retrying: %s", msg)
}

// noteRetry records that the runtime is retrying this handle's session. A message that cannot
// succeed ends the turn: the abort is ours (abortAsked), and runTurn lands TurnFailed with the
// message once /message comes back.
func (h *threadHandle) noteRetry(attempt int, msg string, nextMs int64) {
	h.mu.Lock()
	// Only a turn whose /message this handle has sent is ours: while the pump waits behind
	// another client's turn (an attached TUI's) a retry belongs to that client. A stop in flight
	// has already settled the turn as cancelled. Only a turn still running is accepted: a late
	// event after the terminal state would raise a failure on a finished turn and abort an idle
	// session (and could register an abort the pump has already stopped waiting for).
	if !h.running || !h.turnSent || (h.state != agents.TurnRunning && h.state != agents.TurnQueued) {
		h.mu.Unlock()
		return
	}
	info := &agents.RetryInfo{Attempt: attempt, Message: msg, NextMs: nextMs}
	if h.retryStop != "" {
		h.mu.Unlock()
		return // the abort is already on its way
	}
	stop := attempt >= permanentRetryMinAttempt && permanentRetry(msg)
	if !stop {
		h.retry = info
		h.mu.Unlock()
		return
	}
	info.Failed = true
	h.retry, h.retryFailed = nil, info
	h.retryStop = retryFailure(msg)
	h.abortAsked = true
	addr, ses := h.addr, h.ses
	done := make(chan struct{})
	h.retryAbort = done
	h.mu.Unlock()
	go func() {
		ok := confirmedAbort(addr, ses)
		h.mu.Lock()
		h.retryAbortOK = ok
		h.mu.Unlock()
		close(done)
	}()
}

// retryAbortBackoff separates the attempts to get the automatic abort acknowledged.
var retryAbortBackoff = 500 * time.Millisecond

// confirmedAbort sends the abort until the daemon answers 2xx, at most three times. A transport
// error or a timeout proves nothing: the daemon may still execute a request it received, and a
// later request being acknowledged does not say the earlier one is gone. So one such failure makes
// the whole abort unconfirmed, whatever follows, and the caller treats that as unknown.
func confirmedAbort(addr, ses string) bool {
	lost := false
	for i := 0; i < 3; i++ {
		if i > 0 {
			time.Sleep(retryAbortBackoff)
		}
		res, err := serveClient.Post(abortURL(addr, ses), "application/json", strings.NewReader("{}"))
		if err != nil {
			lost = true
			continue
		}
		res.Body.Close()
		if res.StatusCode/100 == 2 {
			return !lost
		}
	}
	return false
}

// awaitRetryAbort blocks until the abort noteRetry sent has been answered. The pump calls it
// before the next input goes out: an abort still in flight would land on that input's turn
// (same session id) and cut it short with no trace. It reports whether the daemon acknowledged
// the abort (true when none was sent); false means unknown, and the pump must not send on.
func (h *threadHandle) awaitRetryAbort() bool {
	h.mu.Lock()
	done := h.retryAbort
	h.retryAbort = nil
	h.mu.Unlock()
	if done == nil {
		return true
	}
	<-done
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.retryAbortOK
}

// clearRetry drops the live retry note: the retried call produced output, or the turn ended.
func (h *threadHandle) clearRetry() {
	h.mu.Lock()
	h.retry = nil
	h.mu.Unlock()
}

// retryInfo is what the mirror shows: the live retry, or the one that ended the last turn.
// Caller holds h.mu.
func (h *threadHandle) retryInfo() *agents.RetryInfo {
	if h.retry != nil {
		c := *h.retry
		return &c
	}
	if h.retryFailed != nil {
		c := *h.retryFailed
		return &c
	}
	return nil
}
