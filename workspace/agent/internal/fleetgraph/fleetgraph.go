// Package fleetgraph is the Agent-side half of ADR 0096 (docs/log/101): the two
// append-only ledgers behind the fleet session graph (lanes = sessions, x = time) and the
// GET /api/fleet-graph handler that serves them.
//
// The wire shape this package must produce is frozen in console/src/types/fleetgraph.ts —
// this package is not the source of truth for field names, that file is. Two rules from
// there matter everywhere in here: ledger lines are RFC3339 with millisecond precision,
// the DTO is unix millis, and the conversion happens only here, never on the read side;
// and an unrecognised state normalises to "unknown", never "idle" — folding it into idle
// is the dangerous direction (docs/log/101 §101.6).
//
// This package has no dependency on internal/session: every caller (internal/sessionx,
// internal/chatx, package main) passes plain strings, so a session-shaped struct never has
// to round-trip through here and there is no import cycle to manage as those packages grow.
package fleetgraph

import "strings"

// LedgerState is the frozen vocabulary the ledger records (mirrors LedgerState in
// types/fleetgraph.ts exactly — see console/src/lib/fleetgraph.states.json for the parity
// fixture both languages' normalisers are tested against).
type LedgerState string

const (
	StateWorking    LedgerState = "working"
	StateCompacting LedgerState = "compacting"
	StateIdle       LedgerState = "idle"
	StateQuestion   LedgerState = "question"
	StatePlan       LedgerState = "plan"
	StatePermission LedgerState = "permission"
	StateBlocked    LedgerState = "blocked"
	StateAuth       LedgerState = "auth"
	StateLimited    LedgerState = "limited"
	StateSpendLimit LedgerState = "spend_limit"
	StateFailed     LedgerState = "failed"
	StateAborted    LedgerState = "aborted"
	StateUnknown    LedgerState = "unknown"
)

// knownStates is the closed set NormalizeState folds into WITHOUT setting `raw` — every
// literal LedgerState spelling, "unknown" included: a live process reporting the literal
// string "unknown" is a genuine match, not a fallback, so there is no "original spelling"
// to preserve. Keep in lockstep with console/src/lib/fleetgraph.states.json —
// states_parity_test.go fails the suite (not skips it) when that fixture cannot be found,
// per docs/log/101 §101.8.
var knownStates = map[LedgerState]bool{
	StateWorking: true, StateCompacting: true, StateIdle: true, StateQuestion: true,
	StatePlan: true, StatePermission: true, StateBlocked: true, StateAuth: true,
	StateLimited: true, StateSpendLimit: true, StateFailed: true, StateAborted: true,
	StateUnknown: true,
}

// NormalizeState turns a raw live-state spelling into the ledger's closed vocabulary.
// "" becomes idle (Go only ever sees "" for an unset Session.state — never a missing
// value the way the TypeScript side can). Anything this union does not recognise becomes
// StateUnknown, with the original spelling returned as raw so the tooltip can show it —
// folding an unrecognised spelling into idle would silently record a busy stretch as
// nothing happening (ADR 0096 decision 3, the `compacting` near-miss in docs/log/101 §101.7).
//
// raw is set ONLY when normalisation actually fell back to guessing — never when the raw
// spelling already IS a literal member of the union (including the literal string
// "unknown" itself): that is a genuine match, and carrying `raw:"unknown"` alongside
// `state:"unknown"` would claim a fallback that never happened, disagreeing with the
// TypeScript side's own closed-set check on the same fixture.
func NormalizeState(rawSpelling string) (state LedgerState, raw string) {
	if rawSpelling == "" {
		return StateIdle, ""
	}
	s := LedgerState(rawSpelling)
	if knownStates[s] {
		return s, ""
	}
	return StateUnknown, rawSpelling
}

// GraphOrigin is Meta.Origin's frozen vocabulary (ADR 0029 §6 + ADR 0073).
type GraphOrigin string

const (
	OriginUser     GraphOrigin = "user"
	OriginOperator GraphOrigin = "operator"
	OriginSchedule GraphOrigin = "schedule"
	OriginHandoff  GraphOrigin = "handoff"
	OriginSession  GraphOrigin = "session"
	OriginUnknown  GraphOrigin = "unknown"
)

// NormalizeOrigin folds an arbitrary origin string onto the closed vocabulary. Callers
// normally pass session.OriginOf's result, which already does this on the session package's
// own vocabulary, but this package cannot import that one (see the package doc), so it
// re-closes the set on its own.
func NormalizeOrigin(s string) GraphOrigin {
	switch GraphOrigin(s) {
	case OriginUser, OriginOperator, OriginSchedule, OriginHandoff, OriginSession:
		return GraphOrigin(s)
	default:
		return OriginUnknown
	}
}

// excerptMaxRunes bounds InstructEvent/PeerEvent excerpts (ADR 0096 decision 4: at most
// 140 characters, a single line, display-only). Truncation happens here, at write time —
// the frozen contract's render-time sanitisation is about injection, not length.
const excerptMaxRunes = 140

// truncateExcerpt collapses an excerpt to a single line and caps it at excerptMaxRunes,
// marking a cut with an ellipsis so the view does not have to guess whether it is whole.
func truncateExcerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= excerptMaxRunes {
		return s
	}
	return string(r[:excerptMaxRunes-1]) + "…"
}
