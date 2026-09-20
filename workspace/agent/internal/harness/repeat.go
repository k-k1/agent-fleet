package harness

// repeat.go is loop.go's repeated-tool-call gate: a real failure mode measured live
// against qwen3-coder-30b-a3b in an ADR 0093 lcpp harness trial (2026-09,
// /home/dev/lcpp-live/log-coder.txt) — turns 58 through 122, ALL 65 of them, called
// todo_write with byte-identical arguments, one call per turn, nothing else run in
// between. The turn's own prose kept changing (a slightly different-worded "Task
// Completion Summary" each time) but the tool_call underneath it did not, so nothing
// about the surrounding conversation flagged it. loop.go's tool loop had no notion of
// "have I already just done exactly this" at all; this file is that notion. A
// same-shaped comparison run against qwen3.8-27b-uncensored-q4_k_m on the identical
// task (log-qwen38.txt) never repeated a call two turns running, which is what fixes
// this gate's default thresholds well above ordinary legitimate re-checks (re-running
// `go test` after each edit, confirming a build twice) and far below the 65-in-a-row
// failure it exists to catch.

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrRepeatedToolCall is the sentinel behind the error Run returns once the gate's
// abort stage trips (Runtime.RepeatAbortAfter) — callers tell this apart from an
// engine/context/tool error with errors.Is(err, ErrRepeatedToolCall).
var ErrRepeatedToolCall = errors.New("harness: same tool call repeated too many times in a row")

// defaultRepeatWarnAfter and defaultRepeatAbortAfter are Runtime.RepeatWarnAfter/
// RepeatAbortAfter's fallback for <=0 (including a zero Runtime{}). See repeat_test.go
// for both directions this was checked against: the qwen3.8-27b log replayed through
// with these defaults never trips the gate (negative control), and the
// qwen3-coder-30b-a3b log's actual 65-call run does (positive control).
const (
	defaultRepeatWarnAfter  = 3
	defaultRepeatAbortAfter = 8
)

// repeatAction is what the gate decided about one ToolCall, based on the length of
// the identical-call streak it extends.
type repeatAction int

const (
	repeatRun repeatAction = iota
	repeatWarn
	repeatAbort
)

// repeatDecision pairs the action with the streak length it was decided from, so the
// warn-stage message and Result's bookkeeping can report a real count instead of a
// vague "you're repeating yourself".
type repeatDecision struct {
	action repeatAction
	streak int
}

// repeatTracker counts an unbroken run of canonically-identical tool calls. loop.go's
// Run keeps exactly one of these for the whole call, so the streak survives across
// turns — the actual failure being guarded against was never two identical calls in
// the SAME turn, it was the same single call repeated turn after turn.
type repeatTracker struct {
	last  string
	count int
}

// note folds call into the tracker and returns the new streak length: 1 if call
// differs from the immediately preceding one, or one more than the preceding streak
// if it is the same call again.
func (t *repeatTracker) note(call ToolCall) int {
	key := canonicalCall(call)
	if key == t.last {
		t.count++
	} else {
		t.last = key
		t.count = 1
	}
	return t.count
}

// canonicalCall is the gate's definition of "the same call": the tool name plus its
// arguments normalized as JSON — parsed and re-marshaled, so a harmless difference in
// key order cannot hide a repeat behind a byte-level mismatch — rather than a raw
// byte comparison of Arguments. This is deliberately broader than what the
// log-coder.txt incident itself needed (those 65 calls were, as far as this log
// shows, the same words repeated outright) because a raw byte compare would miss the
// same repeat if a future model re-emitted its arguments with different whitespace or
// key order; normalized JSON equality still catches byte-identical repeats too, so
// nothing is given up by comparing this way instead.
func canonicalCall(call ToolCall) string {
	return call.Name + "\x00" + canonicalArgs(call.Arguments)
}

// canonicalArgs re-marshals raw as JSON (encoding/json sorts object keys on Marshal,
// so this doubles as key-order normalization) and falls back to raw itself, verbatim,
// when it does not parse as JSON — a malformed call can still be caught as a repeat if
// the model resends the exact same malformed text.
func canonicalArgs(raw string) string {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return string(b)
}

// repeatGate is a Runtime's RepeatWarnAfter/RepeatAbortAfter/RepeatGateDisabled,
// resolved once per Run (newRepeatGate) rather than re-read from rt on every call.
type repeatGate struct {
	warnAfter, abortAfter int
	disabled              bool
}

// newRepeatGate resolves rt's repeat-gate fields, applying defaultRepeatWarnAfter/
// defaultRepeatAbortAfter to each of RepeatWarnAfter/RepeatAbortAfter independently
// when that field is <=0 — including when rt itself came in as a zero Runtime{}: the
// gate defaults to ON (see RepeatWarnAfter's doc comment in tools.go for why that
// break from ordinary Go zero-value behaviour was deliberate).
func newRepeatGate(rt *Runtime) repeatGate {
	if rt == nil || rt.RepeatGateDisabled {
		return repeatGate{disabled: true}
	}
	warnAfter := rt.RepeatWarnAfter
	if warnAfter <= 0 {
		warnAfter = defaultRepeatWarnAfter
	}
	abortAfter := rt.RepeatAbortAfter
	if abortAfter <= 0 {
		abortAfter = defaultRepeatAbortAfter
	}
	return repeatGate{warnAfter: warnAfter, abortAfter: abortAfter}
}

// decide turns a streak length into what the loop should do about the call that just
// extended it.
func (g repeatGate) decide(streak int) repeatDecision {
	switch {
	case g.disabled:
		return repeatDecision{action: repeatRun, streak: streak}
	case streak >= g.abortAfter:
		return repeatDecision{action: repeatAbort, streak: streak}
	case streak >= g.warnAfter:
		return repeatDecision{action: repeatWarn, streak: streak}
	default:
		return repeatDecision{action: repeatRun, streak: streak}
	}
}

// repeatWarnMessage is the RoleTool content substituted for a call the gate
// intercepted at the warn stage, in place of actually running it — worded as a tool
// error (executeOne's own "error: …" convention for a declined/failed call) so the
// model reacts to it as something that needs a different next step, not as an
// ordinary result it can shrug off.
func repeatWarnMessage(call ToolCall, streak int) string {
	return fmt.Sprintf(
		"error: repeated tool call — %s has now been called with the exact same arguments %d times in a row, with nothing else in between. This call was NOT executed. Stop repeating it: do something different, or explain what is actually still needed.",
		call.Name, streak,
	)
}

// repeatAbortErr is the error Run returns once the gate's abort stage trips.
func repeatAbortErr(call ToolCall, streak int) error {
	return fmt.Errorf("%s called with the exact same arguments %d times in a row, with nothing else in between: %w", call.Name, streak, ErrRepeatedToolCall)
}
