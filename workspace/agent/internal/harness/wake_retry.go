package harness

// wake_retry.go closes a gap ADR 0093 decision 4 promises but Client alone cannot keep: the
// caller's FIRST request is supposed to be the one that gets the answer, without a human having
// to notice a failed turn and resend by hand. Client.Send's streamed path already keeps that
// promise on its own once a stream has actually opened (the Control Plane holds the connection
// open with heartbeats while a box wakes, docs/log/99 §4.11) — but Client.InputTokens (the plain,
// non-streaming call maybeCompact makes before every Send) and an immediate pre-stream refusal on
// Send itself both land on the gateway's OWN 45-second plain-hold (control-plane/
// engine_gateway.go's engineManagedPlainHoldSeconds, capped short of the ingress ALB's 60s idle
// timeout because a non-streaming response has no heartbeat to survive it). Past that hold the
// gateway answers 503 engine_waking and expects the CALLER to ask again — and until now nothing
// here did, so a session's very first turn against a cold box (ADR 0093's own measured 3.5-7
// minute real cold start, docs/log/99 §14) aborted after 45 seconds looking exactly like a real
// failure (found live 2026-09-21, the lcpp kind's first end-to-end run against a stopped engine).
//
// Retried HERE, in the loop that calls Client, rather than inside client.go's own Send/
// InputTokens: a caller can substitute its own Client (the lcpp driver's tests, and this
// package's own scriptedClient, do exactly that), and it needs the same wait — putting the retry
// below the interface would make every fake Client reimplement it to get the same behaviour.

import (
	"context"
	"errors"
	"strings"
	"time"
)

// engineWakeRetryBudget bounds how long a single Send/InputTokens call keeps retrying a wake in
// progress. Comfortably above the ADR's own measured real cold start (3.5-7 minutes), comfortably
// below the gateway's own overall wake timeout (AF_ENGINE_WAKE_TIMEOUT, 900s default) so this side
// gives up before the gateway itself would have declared the start a lost cause. A var, not a
// const, so a test can shrink it rather than actually waiting.
var engineWakeRetryBudget = 10 * time.Minute

// engineWakeRetryInterval is the pause between attempts. Short: the gateway's own plain-path hold
// (45s) or its streamed heartbeat already does the real waiting inside ONE call — this only keeps
// a run of IMMEDIATE refusals (e.g. serve()'s pendingGuard, answered before either path's own wait
// even begins) from spinning. A var for the same reason as the budget above.
var engineWakeRetryInterval = 3 * time.Second

// retryEngineWake calls fn — one Send or InputTokens attempt — retrying while it fails with a
// refusal that means "the engine has not woken up yet" (engineWakeRetryable) and the budget is
// not spent. ctx is the caller's own turn context: an interrupt cancels the wait exactly like it
// cancels everything else this package does.
func retryEngineWake(ctx context.Context, fn func() error) error {
	deadline := time.Now().Add(engineWakeRetryBudget)
	for {
		err := fn()
		if err == nil || !engineWakeRetryable(err) || !time.Now().Before(deadline) {
			return err
		}
		select {
		case <-time.After(engineWakeRetryInterval):
		case <-ctx.Done():
			return err
		}
	}
}

// engineWakeRetryable reports whether err looks like a wake in progress rather than a refusal
// worth surfacing at once. EngineErrorKind's own Retryable() (types.go) covers engine_waking
// outright; the second case matches an engine_unavailable body that names a failure to REACH the
// engine process itself (a Service-Connect DNS record not registered yet, nothing listening on
// the socket) — control-plane's /props route answers exactly this shape for exactly that
// condition ("the engine did not answer /props: "+err.Error(), control-plane/engine_gateway.go),
// and a genuinely first-ever cold start can surface the same wording through the request paths
// this package actually calls. Matched on message text rather than widening Retryable() itself:
// that method's whole point is refusing to retry engine_unavailable's OTHER, administrative
// meaning ("this engine has no enabled model — an administrator has to select one" — retrying on
// a timer would turn a clear refusal into an infinite wait, types.go's own doc comment), and that
// fixed message never contains a network-reachability phrase.
func engineWakeRetryable(err error) bool {
	var ee *EngineError
	if !errors.As(err, &ee) {
		return false
	}
	if ee.Retryable() {
		return true
	}
	if ee.Kind != EngineUnavailable {
		return false
	}
	msg := strings.ToLower(ee.Message)
	for _, needle := range []string{"dial tcp", "no such host", "lookup", "connection refused", "i/o timeout"} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
