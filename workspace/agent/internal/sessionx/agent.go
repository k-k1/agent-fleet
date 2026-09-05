package sessionx

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// The Agent interface and its input/output types live in internal/agents
// (docs/log/23 remaining item 1 Wave C); the opencode implementation is in
// internal/agents/opencode (Wave D), codex in internal/agents/codex (Wave E) and claude in
// internal/agents/claude (Wave F). This file keeps the registry and the shared live-state
// helpers.

// agentRegistry is the kind → agents.Agent registry. AgentOf falls back to claude
// for an unknown or empty kind, matching the historical default (a session with no
// recognized kind launches claude).
var agentRegistry = map[string]agents.Agent{
	session.KindClaude:   claude.New(),
	session.KindOpencode: opencode.New(),
	session.KindCodex:    codex.New(),
	session.KindCursor:   cursor.New(),
	session.KindKiro:     kiro.New(),
	session.KindAgy:      agy.New(),
	session.KindCopilot:  copilot.New(),
	session.KindShell:    shellAgent{},
	session.KindSSM:      ssmAgent{},
}

func AgentOf(kind string) agents.Agent {
	if a, ok := agentRegistry[kind]; ok {
		return a
	}
	return agentRegistry[session.KindClaude]
}

// NormalizeKind maps a create request's kind onto a registered one, defaulting the
// unknown/empty/"claude" cases to claude (the historical create whitelist).
func NormalizeKind(kind string) string {
	if _, ok := agentRegistry[kind]; ok {
		return kind
	}
	return session.KindClaude
}

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

// DriveState is the live state for the drive endpoints (status/output/messages):
// "stopped" when not alive, else idle-or-recorded. heal self-corrects a stale
// non-idle cache when the claude pane is back at its ready prompt (killed+resumed,
// rejected permission, abandoned question) — /output opts out (heal=false) to match
// its historical behavior.
func DriveState(m session.Meta, alive, heal bool) string {
	if !alive {
		return "stopped"
	}
	// opencode: derive state from its own store (the status plugin is unreliable) so the
	// chat chip doesn't stick on "in progress" after a turn the plugin never reported
	// idle for.
	if m.Kind == session.KindOpencode {
		if st := opencode.LiveState(m); st != "" {
			return st
		}
	}
	// agy: no hooks — /input persists an optimistic "working" that nothing clears
	// while an interactive prompt is up (the question/permission widget replaces the
	// idle footer, so the claude-shaped heal below can't fire and the chat showed a
	// blocked session as working). The conversation DB knows the whole state (last step
	// status — agy/pending.go), so ask it first.
	if m.Kind == session.KindAgy {
		if st := agy.LiveState(m); st != "" {
			// Turn end. agy has no Stop hook, so this poll is the only place the
			// completion can be observed — persist idle AND fire the notification,
			// or the operator's completion-report arm is never consumed and the report never
			// arrives (docs/log/30 ②). MarkTurnEnd shares RecordSessionNotification with
			// the hook route, so "which transition counts" stays one implementation.
			// Gated on previous=="working" so repeated polls report once; a duplicate
			// from two concurrent polls is absorbed by handleChatReport's disarm.
			if st == "idle" && status.LiveState(session.UUID(m.Dir, m.Name)) == "working" {
				agents.MarkTurnEnd(session.UUID(m.Dir, m.Name), agents.TurnCompleted)
			}
			return st
		}
	}
	// copilot: no hooks either — the classification derived from events.jsonl is the only
	// state source (state.go; under managed the child process writes the same file, so the
	// two agree). Like agy, this poll is the only place a turn's completion can be observed
	// (the TUI route), so a working→idle transition fires MarkTurnEnd (docs/log/30 ②). Under
	// managed the driver's runTurn already fired it and status is idle — no double fire.
	if m.Kind == session.KindCopilot {
		if st := copilot.LiveState(m); st != "" {
			if st == "idle" && status.LiveState(session.UUID(m.Dir, m.Name)) == "working" {
				agents.MarkTurnEnd(session.UUID(m.Dir, m.Name), agents.TurnCompleted)
			}
			return st
		}
	}
	// cursor: no hooks either — the state source is the classification of the JSONL
	// transcript's tail (state.go). Same shape as copilot: this poll is the only place a
	// turn's completion can be observed (the TUI route), so a working→idle transition fires
	// MarkTurnEnd (docs/log/30 ②). Under managed (Track A2) the driver's runTurn already fired
	// it and status is idle — no double fire.
	if m.Kind == session.KindCursor {
		if st := cursor.LiveState(m); st != "" {
			if st == "idle" && status.LiveState(session.UUID(m.Dir, m.Name)) == "working" {
				agents.MarkTurnEnd(session.UUID(m.Dir, m.Name), agents.TurnCompleted)
			}
			return st
		}
	}
	// kiro: no hooks — 2.14.1 has no Stop hook (the hook triggers are only AgentSpawn/
	// PrePrompt/PreToolUse/PostToolUse; measured, docs/log/43 §5-1), so the state source is the
	// TUI string contract (state.go). Same shape as cursor/copilot: this poll is the only place
	// a turn's completion can be observed (the TUI route), so a working→idle transition fires
	// MarkTurnEnd (docs/log/30 ②). A pending approval ("question") is not idle, so it passes
	// through without firing. Under managed (Track A2) the driver's runTurn already fired it
	// and status is idle — no double fire. When empty (the footer is not drawn yet) it falls
	// back to the generic route (/input's optimistic working).
	if m.Kind == session.KindKiro {
		if st := kiro.LiveState(m); st != "" {
			if st == "idle" && status.LiveState(session.UUID(m.Dir, m.Name)) == "working" {
				agents.MarkTurnEnd(session.UUID(m.Dir, m.Name), agents.TurnCompleted)
			}
			return st
		}
	}
	sid := session.UUID(m.Dir, m.Name)
	// Same resolution as WireLive (status.EffectiveModal): a permission whose question/plan
	// payload has been captured reports itself as the modal the TUI is actually showing
	// (question / plan). The list badge (WireLive) and the chat chip (here) have to show the
	// same state.
	state := status.EffectiveModal(sid, status.LiveState(sid))
	isClaude := NormalizeKind(m.Kind) == session.KindClaude
	// The pane is read exactly once (tmuxx.ReadPane). heal=false (/output) still does not look
	// at the pane, but claude's usage-limit modal has to be reported regardless of heal, so
	// capture whenever either of those two needs it.
	var pane tmuxx.PaneRead
	if heal || isClaude {
		pane = tmuxx.ReadPane(m.Name)
	}
	// claude pins the pane on the usage-limit menu, waiting for a human (see the comment on
	// agents.StateBlocked). The same verdict as WireLive is repeated here because chat and the
	// mirror read this function, and fixing only one side makes the chip in the body disagree
	// with the badge in the list. Rewriting the state (HealIdle) stays on the heal side alone.
	// Expired credentials (docs/log/47 §4-8): the same verdict as WireLive, in the same order
	// (ahead of the usage-limit menu), for the same reason — when the mirror/chat chip and the
	// list badge disagree, the user cannot tell which one is true. For the reason behind the
	// order see the comment in WireLive (report the one waiting will not fix first).
	if isClaude && claude.AuthExpired() {
		if heal && state != "idle" {
			claude.HealIdle(sid)
		}
		return agents.StateAuth
	}
	if isClaude && pane.RateLimitMenu {
		if heal && state != "idle" {
			claude.HealIdle(sid)
		}
		return agents.StateBlocked
	}
	// codex managed: a turn rejected/failed with usageLimitExceeded leaves the
	// session sitting at idle, but re-sending will hit the same limit. Surface it as
	// "waiting for the limit to lift" — there is no menu to dismiss and the window opens
	// by waiting (the same
	// verdict as WireLive).
	if m.DriverKind() == session.DriverManaged && NormalizeKind(m.Kind) == session.KindCodex && state == "idle" && codex.IsRateLimited(m.Name) {
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
		if isClaude {
			claude.HealIdle(sid)
		} else {
			status.Remove(sid)
		}
	} else if heal && state == "idle" && pane.Busy {
		// Reverse-heal: the hook state reads idle (its "working" file was never written,
		// or the self-heal above removed it during a transient prompt frame) but the pane
		// is plainly mid-turn (interrupt affordance shown). Trust the live TUI and persist
		// working so the chat shows "in progress" + the stop button, and the eventual Stop still
		// fires the answer-ready notification (recorded off the previous "working" state).
		state = "working"
		status.Persist(sid, "working")
	}
	// After a turn cut off by a usage limit (the menu has already been dismissed, and a
	// per-model limit shows no menu at all) the pane returns to the ready prompt, so the state
	// so far reads idle. The same reinterpretation as the list (wireSession) is applied here —
	// when the chip in the body disagrees with the list, the user cannot tell which one is true
	// (docs/log/47 §4-9). Sending is not blocked (see the comment on agents.StateLimited).
	if isClaude && alive && state == "idle" {
		if limited, _, waiting := rateLimitWaiting(m, time.Now()); waiting {
			return limited
		}
	}
	return state
}
