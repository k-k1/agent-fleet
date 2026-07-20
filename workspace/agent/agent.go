package main

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// The Agent interface and its input/output types live in internal/agents
// (docs/23 残① Wave C); opencode の実装は internal/agents/opencode（Wave D）、
// codex は internal/agents/codex（Wave E）、claude は internal/agents/claude
// （Wave F）。This file keeps the registry and the shared live-state helpers.

// agentRegistry is the kind → agents.Agent registry. agentOf falls back to claude
// for an unknown or empty kind, matching the historical default (a session with no
// recognized kind launches claude).
var agentRegistry = map[string]agents.Agent{
	session.KindClaude:   claude.New(),
	session.KindOpencode: opencode.New(),
	session.KindCodex:    codex.New(),
	session.KindAgy:      agy.New(),
	session.KindShell:    shellAgent{},
	session.KindSSM:      ssmAgent{},
}

func agentOf(kind string) agents.Agent {
	if a, ok := agentRegistry[kind]; ok {
		return a
	}
	return agentRegistry[session.KindClaude]
}

// normalizeKind maps a create request's kind onto a registered one, defaulting the
// unknown/empty/"claude" cases to claude (the historical create whitelist).
func normalizeKind(kind string) string {
	if _, ok := agentRegistry[kind]; ok {
		return kind
	}
	return session.KindClaude
}

// --- shared live-state helpers -------------------------------------------------

// sessionAlive is the driver-aware liveness test: tui = the tmux session exists,
// managed = a live runtime handle exists（docs/27 P2）。wireSession の alive 引数を
// 埋める全ハンドラはこれを使う（tmuxx.HasSession 直叩きは managed を常に停止扱い
// にしてしまう）。
func sessionAlive(m session.Meta) bool {
	if m.DriverKind() == session.DriverManaged {
		return managedAlive(m)
	}
	return tmuxx.HasSession(session.TmuxName(m.Name))
}

// driveState is the live state for the drive endpoints (status/output/messages):
// "stopped" when not alive, else idle-or-recorded. heal self-corrects a stale
// non-idle cache when the claude pane is back at its ready prompt (killed+resumed,
// rejected permission, abandoned question) — /output opts out (heal=false) to match
// its historical behavior.
func driveState(m session.Meta, alive, heal bool) string {
	if !alive {
		return "stopped"
	}
	// opencode: derive state from its own store (the status plugin is unreliable) so the
	// chat chip doesn't stick on 進行中 after a turn the plugin never reported idle for.
	if m.Kind == session.KindOpencode {
		if st := opencode.LiveState(m); st != "" {
			return st
		}
	}
	// agy: no hooks — /input persists an optimistic "working" that nothing clears
	// while an interactive prompt is up (the question/permission widget replaces the
	// idle footer, so the claude-shaped heal below can't fire and the chat showed a
	// blocked session as 作業中). The conversation DB knows (last step status=9 —
	// agy/pending.go), so ask it first and report question/permission.
	if m.Kind == session.KindAgy {
		if st, _ := agy.Probe(m); st != "" {
			return st
		}
	}
	sid := session.UUID(m.Dir, m.Name)
	state := status.LiveState(sid)
	if heal && state != "idle" && tmuxx.AtIdlePrompt(m.Name) {
		state = "idle"
		status.Remove(sid)
	} else if heal && state == "idle" && tmuxx.IsBusy(m.Name) {
		// Reverse-heal: the hook state reads idle (its "working" file was never written,
		// or the self-heal above removed it during a transient prompt frame) but the pane
		// is plainly mid-turn (interrupt affordance shown). Trust the live TUI and persist
		// working so the chat shows 進行中 + the stop button, and the eventual Stop still
		// fires the answer-ready notification (recorded off the previous "working" state).
		state = "working"
		status.Persist(sid, "working")
	}
	return state
}
