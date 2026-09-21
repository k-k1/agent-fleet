// Package muse is the vertical package for the muse kind (ADR 0095): Meta's Muse Code,
// driven over the Muse Session Protocol instead of a TUI pane. The protocol client itself is
// internal/msp; this package is the Agent Fleet side — the read-layer registration here, the
// managed driver in driver.go, and the per-session thread in handle.go.
//
// muse is managed-only (decision 2): MSP already carries statuses, approvals, steering, fork,
// usage and the model list, so a pane would buy a second UI and a string contract to maintain.
// BuildLaunch therefore always fails and Caps.ManagedOnly is set.
package muse

import (
	"errors"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// ErrNoTerminalRoute is BuildLaunch's unconditional refusal (decision 2).
var ErrNoTerminalRoute = errors.New("Muse Code セッションには Terminal(CLI) 実行方式がありません（managed 専用の kind です）")

// New returns the muse Agent implementation for the kind registry.
func New() agents.Agent { return agentImpl{} }

type agentImpl struct{}

func (agentImpl) Kind() string { return session.KindMuse }

// Caps. ManagedOnly is decision 2. PermissionChoice is true because decision 5 keeps
// approvals on and the driver answers them from the Console — without it POST /sessions
// refuses skip_permissions=false for this kind, so the launch flow could not even ask.
//
// CanTranscript, CanFork and CanForkAt stay false for now. MSP carries `session/fork` and the
// transcript rides `item/*` plus the at-rest JSONL, so all three will be true — but a cap is
// a claim about a path that was measured end to end (docs/log/76), and neither the transcript
// reader nor the fork path exists yet. Claiming one early shows the member an affordance that
// silently does nothing.
func (agentImpl) Caps() agents.Caps {
	return agents.Caps{
		ManagedOnly:      true,
		PermissionChoice: true,
	}
}

// BuildLaunch always fails: muse has no tmux pane program (decision 2).
func (agentImpl) BuildLaunch(session.Meta, agents.LaunchOpts) (agents.LaunchPlan, error) {
	return agents.LaunchPlan{}, ErrNoTerminalRoute
}

// WireLive rides the same generic status-store route every hook-driven kind uses: the driver
// writes status through agents.MarkTurnStart/MarkTurnEnd, so nothing is polled here.
func (agentImpl) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	li := agents.LiveInfo{Resumable: true}
	if !alive {
		return li
	}
	sid := slotSid(m)
	li.State = status.EffectiveModal(sid, status.LiveState(sid))
	return li
}

// ClearResume forgets the muse session id captured for this slot, so a recreate starts a
// fresh conversation instead of trying to reload one.
//
// It is belt to the braces rather than the mechanism: an MSP session id is good exactly once
// (a retained or reserved id is refused `session_id_conflict`), and the store is keyed by the
// slot sid, which is a pure function of (dir, name) — a recreate mints a new Name, so it
// structurally cannot inherit the entry. What this guards is the caller that reuses a name.
func (agentImpl) ClearResume(sid string) { sessions.Remove(sid) }

// Transcript has no generic source yet: the live items and the at-rest session.jsonl reader
// are the next work package. Returning ok=false is what the read layer does for a kind
// without one, and it is the honest answer until the reader exists.
func (agentImpl) Transcript(session.Meta) (agents.TranscriptData, bool) {
	return agents.TranscriptData{}, false
}
