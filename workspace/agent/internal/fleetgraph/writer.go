package fleetgraph

import (
	"sync"
	"time"
)

// Birth describes a new lane's first line (ADR 0096 decision 13). At is normally left
// zero (the current instant is used); the genesis backfill (backfill.go's caller in
// internal/sessionx) sets it to Meta.CreatedAt so a pre-existing session's birth lands at
// when it actually happened, not at Agent boot.
type Birth struct {
	Name, Kind, Repo string
	Origin           GraphOrigin
	OriginSession    string
	// Conv is this session's OWN conversation id, resolved the same way its kind's
	// Forker.ForkSource resolves it — never the id AF handed it at launch (docs/log/101
	// §101.5.1). Leave empty when the kind cannot resolve one yet; a later RecordConvID
	// fills it in.
	Conv     string
	ForkFrom string
	Display  string
	At       time.Time
}

// RecordBirth appends a birth lineage line. Called from every place a NEW session name is
// minted: create, fork, and recreate (docs/log/101 §101.6, write site ①) — recreate mints a
// fresh slug/sid in the same way create does, so from the graph's perspective it is another
// birth, not a special case.
func RecordBirth(b Birth) {
	if b.Name == "" {
		return
	}
	ts := stampNow()
	if !b.At.IsZero() {
		ts = b.At.Format(rfc3339Milli)
	}
	line := lineageLine{
		Ev: "birth", Ts: ts, Name: b.Name, Kind: b.Kind, Repo: b.Repo,
		Origin: string(b.Origin), OriginSession: b.OriginSession,
		Conv: b.Conv, ForkFrom: b.ForkFrom, Display: b.Display,
	}
	appendLine(&lineageMu, lineagePath(), line)
}

// RecordConvID appends a convid line: this lane's conversation id changed, or became
// resolvable for the first time (claude's own id drift when it relaunches itself and
// --session-id drops out of argv, internal/agents/claude/sid.go).
func RecordConvID(name, conv string) {
	if name == "" || conv == "" {
		return
	}
	appendLine(&lineageMu, lineagePath(), lineageLine{Ev: "convid", Ts: stampNow(), Name: name, Conv: conv})
}

// RecordDeath appends the end of one run, AS OBSERVED (docs/log/101 §101.6: Meta.StoppedAt
// is filled lazily, so this is "first seen gone", not "when it happened"). reason is one
// of oom/killed/crashed, or "" for a clean/deliberate stop.
func RecordDeath(name, reason string, code, signal int) {
	RecordDeathAt(name, reason, code, signal, time.Time{})
}

// RecordDeathAt is RecordDeath with an explicit instant — only the genesis backfill needs
// this (it back-dates to Meta.StoppedAt rather than "now").
func RecordDeathAt(name, reason string, code, signal int, at time.Time) {
	if name == "" {
		return
	}
	ts := stampNow()
	if !at.IsZero() {
		ts = at.Format(rfc3339Milli)
	}
	appendLine(&lineageMu, lineagePath(), lineageLine{
		Ev: "death", Ts: ts, Name: name, Reason: reason, Code: code, Signal: signal,
	})
}

// RecordRevive appends a revive line: the SAME lane started another run (ADR 0096
// decision 12). The condition is "the slot became alive again" — never "something cleared
// Meta.StoppedAt", because HandleRestoreSession clears that field without actually starting
// anything (docs/log/101 §101.8; it only ever gets an ArchivedEvent{Archived:false}).
func RecordRevive(name string) {
	if name == "" {
		return
	}
	appendLine(&lineageMu, lineagePath(), lineageLine{Ev: "revive", Ts: stampNow(), Name: name})
}

// RecordArchived appends an archive/restore line. archived=false is the restore.
func RecordArchived(name string, archived bool) {
	if name == "" {
		return
	}
	a := archived
	appendLine(&lineageMu, lineagePath(), lineageLine{Ev: "archived", Ts: stampNow(), Name: name, Archived: &a})
}

// RecordInstruct appends an instruction-arrived line. to is the receiving lane; from is
// the sending actor (a lane id, or one of the non-lane spellings in ActorId's vocabulary —
// "conv:<id>" / "user" / "schedule" / "bridge:discord" / "bridge:slack" / "agent"). excerpt
// is truncated to a single ≤140-char line here (ADR 0096 decision 4).
func RecordInstruct(from, to, source, excerpt string) {
	if to == "" {
		return
	}
	appendLine(&activityMu, activityPath(utcDay(clockNow())), activityLine{
		Ev: "instruct", Ts: stampNow(), From: from, To: to, Source: source, Excerpt: truncateExcerpt(excerpt),
	})
}

// RecordReport appends a report-left-a-session line. kind mirrors chatx's ReportKind
// vocabulary ("answer-ready", …); reason (oom/crashed/killed) draws red in the view.
func RecordReport(from, to, kind, reason string) {
	if from == "" || to == "" {
		return
	}
	appendLine(&activityMu, activityPath(utcDay(clockNow())), activityLine{
		Ev: "report", Ts: stampNow(), From: from, To: to, Kind: kind, Reason: reason,
	})
}

// RecordPeer appends a session-to-session message line (ADR 0041). It is here and NOT on
// instr-ledger by construction — a peer send must never touch the arm (0041 decision 4).
func RecordPeer(from, to, intent, excerpt string) {
	if from == "" || to == "" {
		return
	}
	appendLine(&activityMu, activityPath(utcDay(clockNow())), activityLine{
		Ev: "peer", Ts: stampNow(), From: from, To: to, Intent: intent, Excerpt: truncateExcerpt(excerpt),
	})
}

// --- Live-state observation (ADR 0096 decision 3) ---------------------------------------

// stateMu guards lastState AND serialises the state-line append that follows a change, so
// the two observers driving GET /sessions (a Console at 4s, the control plane's idle-stop
// reaper at 1m — docs/log/101 §101.2) can hit ObserveState concurrently for the same
// session without ever producing two lines for one transition. A single process-wide lock
// rather than one per session: state changes across the whole fleet are, at most, a few per
// second, so the extra granularity would buy nothing.
var stateMu sync.Mutex
var lastState = map[string]LedgerState{}

// ObserveState records a live-state transition where the session list already computes
// state for every session (ADR 0096 decision 3): no new polling, no transcript scan. Writes
// ONLY when the normalised state differs from the last one this process observed for name.
// `from` is omitted when name has no entry yet (after a restart, or a session seen for the
// first time mid-life) — that means "unknown before this", never "idle before this", so
// callers must seed the map with ResyncAll at boot before relying on `from`.
func ObserveState(name, rawSpelling string) {
	if name == "" {
		return
	}
	state, raw := NormalizeState(rawSpelling)
	stateMu.Lock()
	defer stateMu.Unlock()
	prev, had := lastState[name]
	if had && prev == state {
		return
	}
	lastState[name] = state
	line := activityLine{Ev: "state", Ts: stampNow(), Name: name, To: string(state), Raw: raw}
	if had {
		line.From = string(prev)
	}
	appendLine(&activityMu, activityPath(utcDay(clockNow())), line)
}

// ResyncAll writes one resync line per session right after an Agent restart and reseeds
// the last-observed-state map from it (ADR 0096 decision 3): it marks the boundary of an
// observation gap, so the stretch before it stays UNKNOWN rather than being back-filled
// with a guess, and it is what lets ObserveState's `from` be trusted immediately after boot.
func ResyncAll(rawStates map[string]string) {
	stateMu.Lock()
	defer stateMu.Unlock()
	lastState = make(map[string]LedgerState, len(rawStates))
	ts := stampNow()
	for name, rawSpelling := range rawStates {
		if name == "" {
			continue
		}
		state, raw := NormalizeState(rawSpelling)
		lastState[name] = state
		appendLine(&activityMu, activityPath(utcDay(clockNow())), activityLine{
			Ev: "resync", Ts: ts, Name: name, To: string(state), Raw: raw,
		})
	}
}
