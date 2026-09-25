package harness

// repeat.go is loop.go's repeated-tool-call gate: a real failure mode measured live
// against qwen3-coder-30b-a3b in an ADR 0093 lcpp harness trial (2026-09,
// /home/dev/lcpp-live/log-coder.txt) — turns 50 through 121, 72 turns straight
// (verified by counting the log's own `names=[...]` column; todo_write appears 78
// times across all 162 turns, but this is the one unbroken run), called todo_write
// with NOTHING ELSE run in between. What the log actually records is tool NAMES and
// the assistant's own prose (a slightly different-worded "Task Completion Summary"
// each time), not the raw argument JSON — so whether the arguments were themselves
// identical each time is UNVERIFIED. That is a real, open risk for this gate as
// built: canonicalCall (below) keys on name AND arguments, so if the real incident's
// 72 calls actually varied their arguments turn to turn, THIS GATE WOULD NOT HAVE
// CAUGHT THE ACTUAL INCIDENT. It is deliberately built this way anyway (name-only
// matching would flag legitimate cases like re-running `go test` after each of
// several unrelated edits), on the judgement that a tool call carrying genuinely
// fresh information changes its arguments almost by definition — but that judgement
// is not itself verified against this log. loop.go's tool loop had no notion of
// "have I already just done exactly this" at all; this file is that notion. A
// same-shaped comparison run against qwen3.8-27b-uncensored-q4_k_m on the identical
// task (log-qwen38.txt) never repeated the same tool name two turns running, which is
// what fixes this gate's default thresholds well above ordinary legitimate re-checks
// (re-running `go test` after each edit, confirming a build twice) and far below the
// 72-in-a-row failure it exists to catch.
//
// That risk came true (ADR 0093 debt 2): on later live runs the gate fired zero times
// while coder called one tool six turns running with different arguments each time. The
// name-streak half of this file (nameStreakTracker) is the answer: it ignores arguments
// and counts consecutive assistant turns that call only one tool name, with thresholds
// set from every trial log in that series — the longest such run in a PASSING session
// was 6 turns (gemma-4 `bash`, gpt-oss `read`), the incident was 72. Because the
// arguments differ, each call may be genuine progress, so its warn stage still runs the
// call and only prefixes the result with a notice; its abort stage stops Run exactly
// like the exact-call gate's.
//
// Known limitation: both halves only ever look at an unbroken RUN (anything different in
// between resets them). An A, B, A, B, ... alternating pattern is never flagged, no
// matter how long it runs — not what the measured incidents did, but a gap left open on
// purpose rather than by oversight.

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrRepeatedToolCall is the sentinel behind the error Run returns once the gate's
// abort stage trips (Runtime.RepeatAbortAfter) — callers tell this apart from an
// engine/context/tool error with errors.Is(err, ErrRepeatedToolCall). Unlike the
// ctx/Send failure paths in loop.go (which really cannot continue), this one is
// meant to be recoverable: Result.Messages already has a gate error attached to
// every one of the aborting turn's calls (repeatAbortToolMessage), so it is a
// sendable history a caller can hand straight back into another Run call to tell the
// model to do something else, not a truncated one.
var ErrRepeatedToolCall = errors.New("harness: same tool call repeated too many times in a row")

// defaultRepeatWarnAfter and defaultRepeatAbortAfter are Runtime.RepeatWarnAfter/
// RepeatAbortAfter's fallback for <=0 (including a zero Runtime{}). See repeat_test.go
// for both directions this was checked against: a reconstruction of the qwen3.8-27b
// log's call sequence never trips the gate with these defaults (negative control),
// and a reconstruction of the qwen3-coder-30b-a3b incident's 72-call tool-name streak
// does (positive control) — see this file's own top comment for why "reconstruction"
// is the honest word: the incident log does not preserve raw argument JSON.
const (
	defaultRepeatWarnAfter  = 3
	defaultRepeatAbortAfter = 8
)

// defaultRepeatNameWarnAfter and defaultRepeatNameAbortAfter are Runtime.
// RepeatNameWarnAfter/RepeatNameAbortAfter's fallback for <=0. Warn is twice the longest
// same-name run measured in a passing session (6 turns); abort stays far below the 72-turn
// incident. Lowering either toward 6 starts interrupting sessions that were working.
const (
	defaultRepeatNameWarnAfter  = 12
	defaultRepeatNameAbortAfter = 20
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
// byte comparison of Arguments. A raw byte compare would miss a repeat if a model
// re-emitted its arguments with different whitespace or key order; normalized JSON
// equality still catches byte-identical repeats too, so nothing is given up by
// comparing this way instead. Whether the log-coder.txt incident's 72 calls actually
// needed this (vs. would have matched on raw bytes anyway) is NOT known — see this
// file's own top comment: the log does not preserve the argument JSON at all, only
// the tool name.
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

// nameStreakTracker counts consecutive assistant turns whose tool calls all use one
// tool name, whatever their arguments. It is kept per turn, not per call: one turn that
// reads eight files in parallel is a single step, and the live logs the defaults come
// from were counted in turns. A turn mixing tool names breaks the streak.
type nameStreakTracker struct {
	name  string
	count int
}

// note folds one turn's calls into the tracker and returns the new streak length (0
// for a turn that mixes tool names).
func (t *nameStreakTracker) note(calls []ToolCall) int {
	name := calls[0].Name
	for _, c := range calls[1:] {
		if c.Name != name {
			t.name, t.count = "", 0
			return 0
		}
	}
	if name == t.name {
		t.count++
	} else {
		t.name, t.count = name, 1
	}
	return t.count
}

// repeatGate is a Runtime's repeat-gate fields, resolved once per Run (newRepeatGate)
// rather than re-read from rt on every call.
type repeatGate struct {
	warnAfter, abortAfter         int
	nameWarnAfter, nameAbortAfter int
	disabled                      bool
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
	nameWarnAfter := rt.RepeatNameWarnAfter
	if nameWarnAfter <= 0 {
		nameWarnAfter = defaultRepeatNameWarnAfter
	}
	nameAbortAfter := rt.RepeatNameAbortAfter
	if nameAbortAfter <= 0 {
		nameAbortAfter = defaultRepeatNameAbortAfter
	}
	return repeatGate{
		warnAfter: warnAfter, abortAfter: abortAfter,
		nameWarnAfter: nameWarnAfter, nameAbortAfter: nameAbortAfter,
	}
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

// decideName is decide's counterpart for a same-name turn streak.
func (g repeatGate) decideName(streak int) repeatAction {
	switch {
	case g.disabled:
		return repeatRun
	case streak >= g.nameAbortAfter:
		return repeatAbort
	case streak >= g.nameWarnAfter:
		return repeatWarn
	default:
		return repeatRun
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

// repeatAbortToolMessage is the RoleTool content Run attaches, for EVERY call in the
// turn that tripped the abort stage, before returning — not just the one call whose
// streak actually crossed RepeatAbortAfter. Without this, hist would end on an
// assistant message with unanswered ToolCalls, which is not a shape safe to resend
// (compact.go has hit chat-template rejections from less than this: an unanswered
// user turn collapsed by summarization, a system message stranded mid-history). A
// caller that wants to keep the conversation going after ErrRepeatedToolCall — "tell
// the model to do something else and continue", the abort stage's whole reason for
// being recoverable rather than just fatal — can hand Result.Messages straight back
// to a fresh Run call and get a sendable request, not a malformed one.
func repeatAbortToolMessage(call, aborted ToolCall, streak int) string {
	if call.ID == aborted.ID {
		return fmt.Sprintf(
			"error: repeated tool call — %s has now been called with the exact same arguments %d times in a row, with nothing else in between. This session has been stopped; this call was NOT executed.",
			call.Name, streak,
		)
	}
	return fmt.Sprintf(
		"error: not executed — this turn was stopped because %s was called with the exact same arguments %d times in a row, with nothing else in between.",
		aborted.Name, streak,
	)
}

// repeatNameWarnNotice is prefixed to the real result of each call in a turn that
// extended a same-name streak past the warn stage. The call did run — its arguments
// differ, so this may be genuine progress — which is why this is a notice in front of
// the output rather than an "error:" in place of it.
func repeatNameWarnNotice(name string, turns int) string {
	return fmt.Sprintf(
		"note: %s has now been the only tool called for %d turns in a row (with different arguments each time). If this is not getting closer to the goal, stop and change approach, or answer with what you have.\n\n",
		name, turns,
	)
}

// repeatNameAbortErr is the error Run returns once a same-name streak reaches
// Runtime.RepeatNameAbortAfter.
func repeatNameAbortErr(name string, turns int) error {
	return fmt.Errorf("%s was the only tool called for %d turns in a row: %w", name, turns, ErrRepeatedToolCall)
}

// repeatNameAbortToolMessage answers every call of the turn that tripped the name
// abort, for the same sendable-history reason as repeatAbortToolMessage.
func repeatNameAbortToolMessage(name string, turns int) string {
	return fmt.Sprintf(
		"error: not executed — %s has been the only tool called for %d turns in a row. This session has been stopped.",
		name, turns,
	)
}
