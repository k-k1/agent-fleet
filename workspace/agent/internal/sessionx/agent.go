package sessionx

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// The kind registry is internal/agentreg (kind → agents.Agent) over internal/kinds (the
// capability-free list of the same kinds). The functions below are its names in sessionx, kept
// so the callers here and in package main read as before. This file also keeps the shared
// live-state helpers.

// SessionTurns reads a session's transcript through its kind's agent, Terminal and Managed
// alike: the mirror's source, and chatx's for a scheduled run's answer (#1560).
func SessionTurns(m session.Meta) ([]transcript.Turn, bool) {
	td, ok := AgentOf(m.Kind).Transcript(m)
	return td.Turns, ok
}

// AgentOf is agentreg.Of: the kind's agent, or claude's for an unknown or empty kind.
func AgentOf(kind string) agents.Agent { return agentreg.Of(kind) }

// RegisteredAgent is agentreg.Lookup: AgentOf without the claude fallback.
func RegisteredAgent(kind string) (agents.Agent, bool) { return agentreg.Lookup(kind) }

// RegisteredKinds is agentreg.Kinds: every registered kind, sorted.
func RegisteredKinds() []string { return agentreg.Kinds() }

// NormalizeKind is agentreg.Normalize, the read-side fallback to claude.
func NormalizeKind(kind string) string { return agentreg.Normalize(kind) }

// ValidateCreateKind is agentreg.ValidateCreate, the create paths' exact-match gate.
func ValidateCreateKind(kind string) error { return agentreg.ValidateCreate(kind) }

// ManagedDefaultKind is agentreg.ManagedDefault.
func ManagedDefaultKind(kind string) bool { return agentreg.ManagedDefault(kind) }

// --- shared live-state helpers -------------------------------------------------

// SessionAlive is the driver-aware liveness test: tui = the tmux session exists,
// managed = a live runtime handle exists (docs/log/27 P2). Every handler that fills
// wireSession's alive argument goes through this — calling tmuxx.HasSession directly would
// report every managed session as stopped.
func SessionAlive(m session.Meta) bool {
	if m.DriverKind() == session.DriverManaged {
		return ManagedAlive(m)
	}
	return tmuxx.HasSession(session.TmuxName(m.Name))
}

// readPane is tmuxx.ReadPane, replaceable in tests.
var readPane = tmuxx.ReadPane

// DriveState is the live state for the drive endpoints (status/output/messages):
// "stopped" when not alive, else idle-or-recorded. heal self-corrects a stale
// non-idle cache when the claude pane is back at its ready prompt (killed+resumed,
// rejected permission, abandoned question) — /output opts out (heal=false) to match
// its historical behavior.
func DriveState(m session.Meta, alive, heal bool) string {
	if !alive {
		return "stopped"
	}
	// The kind's own verdict comes first: opencode's store (its status plugin is unreliable),
	// agy's conversation DB, the copilot/cursor/kiro classifications and muse's live handle. A
	// kind without hooks keeps the optimistic "working" /input stored without it, and muse's
	// status file holds only turn start and end, so a waiting prompt would read "working".
	live := liveStateTraits(m.Kind)
	if live.State != nil {
		if st := live.State(m); st != "" {
			// Turn end. A kind with no Stop hook observes its completion only in this poll, so
			// it persists idle AND fires the notification, or the operator's completion-report
			// arm is never consumed and the report never arrives (docs/log/30 ②). MarkTurnEnd
			// shares RecordSessionNotification with the hook route, so "which transition
			// counts" stays one implementation. Gated on previous=="working" so repeated polls
			// report once; a duplicate from two concurrent polls is absorbed by
			// handleChatReport's disarm. Under managed the driver's runTurn already fired it
			// and status is idle — no double fire. (The sessions list observes the same end,
			// but only RECORDS when it happened — turn_end_poll.go.)
			if live.PolledTurnEnd {
				notifyPolledTurnEnd(m, st)
			}
			return st
		}
		// No opinion: also what agy's stale "working" (withdrawn after an hour with no tool
		// running) turns into. The stored optimistic "working" must not stand in for it past
		// the warm-up, or the chip says in progress forever; nothing is persisted and no
		// notification fires, so a turn that really finishes later still reports.
		if live.StaleWorkingIdle {
			sid := session.UUID(m.Dir, m.Name)
			if status.LiveState(sid) == "working" && !status.RecentlyWorking(sid) {
				return "idle"
			}
		}
	}
	sid := session.UUID(m.Dir, m.Name)
	// Same resolution as WireLive (status.EffectiveModal): a permission whose question/plan
	// payload has been captured reports itself as the modal the TUI is actually showing
	// (question / plan). The list badge (WireLive) and the chat chip (here) have to show the
	// same state.
	state := status.EffectiveModal(sid, status.LiveState(sid))
	// codex: an Esc interrupt fires no Stop hook, so "working" outlives the turn. The badge
	// (WireLive) reads the turn's end off the rollout; the chip reads the same.
	exact := terminalTraits(m.Kind)
	if exact.MissedTurnEnd != nil && state == "working" && exact.MissedTurnEnd(m) {
		state = "idle"
	}
	// codex (Terminal): its hooks report only working/idle, so the request_user_input dialog
	// leaves the turn "working". The badge (WireLive) and the free-text gate read the rollout
	// for it; the chip reads the same, or it says "in progress" while sends are refused with
	// question_pending.
	if exact.TerminalQuestion != nil && state == "working" && exact.TerminalQuestion(m) != "" {
		return "question"
	}
	// claude's traits (an empty kind reads as claude): the usage-limit menu, the expired login
	// and the heal that reports a turn that died on an API error.
	t := normalizedTerminalTraits(m.Kind)
	usageLimits := t.UsageLimitMenu != nil
	// The pane is read exactly once (tmuxx.ReadPane). heal=false (/output) still does not look
	// at the pane, but claude's usage-limit modal has to be reported regardless of heal, so
	// capture whenever either of those two needs it.
	var pane tmuxx.PaneRead
	if heal || usageLimits {
		pane = readPane(m.Name)
	}
	// claude pins the pane on the usage-limit menu, waiting for a human (see the comment on
	// agents.StateBlocked). The same verdict as WireLive is repeated here because chat and the
	// mirror read this function, and fixing only one side makes the chip in the body disagree
	// with the badge in the list. Rewriting the state (HealIdle) stays on the heal side alone.
	// Expired credentials (docs/log/47 §4-8): the same verdict as WireLive, in the same order
	// (ahead of the usage-limit menu), for the same reason — when the mirror/chat chip and the
	// list badge disagree, the user cannot tell which one is true. For the reason behind the
	// order see the comment in WireLive (report the one waiting will not fix first).
	if callTrue0(t.AuthExpired) {
		if heal && state != "idle" {
			healIdle(t, sid)
		}
		return agents.StateAuth
	}
	// A recoverer kind's (codex) turn that died on the login: the same verdict as its WireLive,
	// for an idle session only (a turn running now is a resume that is already under way).
	if state == "idle" && authBlockedFor(m) {
		return agents.StateAuth
	}
	if usageLimits && pane.RateLimitMenu {
		if heal && state != "idle" {
			healIdle(t, sid)
		}
		return agents.StateBlocked
	}
	// A turn rejected or failed on a usage limit (codex Managed, muse) leaves the session
	// sitting at idle, but re-sending will hit the same limit. Surface it as "waiting for the
	// limit to lift": there is no menu to dismiss and the window opens by waiting (the same
	// verdict as WireLive).
	if live.RateLimited != nil && state == "idle" &&
		(!live.RateLimitedManagedOnly || m.DriverKind() == session.DriverManaged) && live.RateLimited(m.Name) {
		return agents.StateLimited
	}
	// Folding on pane.IdleSettled (not the raw pane.Idle) for the same reason as WireLive:
	// while claude renders an answer it draws the same picture as the ready prompt, so a single
	// frame cannot tell them apart (measured, tmuxx/idlesettle.go). Leaving Idle here would put
	// the mirror/chat chip on "waiting for input" while the list still reads "in progress".
	if heal && state != "idle" && pane.IdleSettled {
		state = "idle"
		// claude can tell "the turn died on an API error" from the transcript's tail, so that
		// one case is reported as a terminating event instead of being cleared silently
		// (docs/log/47). The evidence is specific to claude's jsonl format, so every other kind
		// only removes the marker. NormalizeKind: an old session with an empty kind is claude
		// too, and a raw comparison would miss it.
		healIdle(t, sid)
	} else if heal && state == "idle" && pane.Busy && claude.ReopenFromPane(sid) {
		// Reverse-heal: the hook state reads idle (its "working" file was never written,
		// or the self-heal above removed it during a transient prompt frame) but the pane
		// is plainly mid-turn (interrupt affordance shown). Trust the live TUI and persist
		// working so the chat shows "in progress" + the stop button, and the eventual Stop still
		// fires the answer-ready notification (recorded off the previous "working" state).
		// A turn the Stop hook closed is not reopened here: see claude.PaneMayReopen.
		state = "working"
	}
	// After a turn cut off by a usage limit (the menu has already been dismissed, and a
	// per-model limit shows no menu at all) the pane returns to the ready prompt, so the state
	// so far reads idle. The same reinterpretation as the list (wireSession) is applied here —
	// when the chip in the body disagrees with the list, the user cannot tell which one is true
	// (docs/log/47 §4-9). Sending is not blocked (see the comment on agents.StateLimited).
	if usageLimits && alive && state == "idle" {
		if limited, _, waiting := rateLimitWaiting(m, time.Now()); waiting {
			return limited
		}
	}
	return state
}
