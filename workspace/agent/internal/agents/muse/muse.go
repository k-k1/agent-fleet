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
// CanTranscript is true now that Transcript below really reads a store that the live item
// stream fills. CanFork and CanForkAt stay false: MSP carries `session/fork`, so both will be
// true, but a cap is a claim about a path measured end to end (docs/log/76) and that one is
// not built — claiming it early shows the member an affordance that silently does nothing.
func (agentImpl) Caps() agents.Caps {
	return agents.Caps{
		ManagedOnly:      true,
		PermissionChoice: true,
		CanTranscript:    true,
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
func (agentImpl) ClearResume(sid string) {
	sessions.Remove(sid)
	openStore(sid).Remove()
}

// Transcript reads AF's own item store, live handle or not — a stopped session shows the same
// history a running one does, because the store is on disk either way.
//
// It must stay a plain disk read: this is called from the usage aggregation as well as the
// mirror, so spawning a host to ask `session/read` would turn a fleet-wide usage query into
// one 73 MiB process per muse session (transcript.go's own header explains why muse's
// at-rest file is not an option).
func (agentImpl) Transcript(m session.Meta) (agents.TranscriptData, bool) {
	st := openStore(slotSid(m))
	items, err := st.Items()
	if err != nil {
		return agents.TranscriptData{}, false
	}
	td := agents.TranscriptData{Turns: turnsFromItems(items), Path: st.Path(), Mode: "normal"}

	h := handleFor(m.Name)
	if h == nil {
		return td, true
	}
	// Overlay what only the live handle knows: the fragments of an item still streaming, the
	// prompt awaiting an answer, and the prompts queued behind the running turn.
	if fragments := h.streamingText(); len(fragments) > 0 {
		td.Turns = appendStreaming(td.Turns, items, fragments)
	}
	h.mu.Lock()
	if h.inter != nil {
		// The two channels go out separately: an approval blocks a tool and takes
		// allow/deny, a question takes an answer, and a surface that confused them would
		// render the wrong control.
		td.Pending = h.inter.Questions
		if h.inter.Kind == agents.InteractionApproval {
			td.PendingApproval, td.PendingApprovalID = h.inter.Approval, h.inter.ID
		}
	}
	for _, in := range h.queue {
		td.Queued = append(td.Queued, in.Prompt)
	}
	h.mu.Unlock()
	return td, true
}
