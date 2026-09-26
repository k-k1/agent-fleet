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
	"fmt"

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

// Caps. ManagedOnly is decision 2.
//
// 🔴 PermissionChoice is FALSE, and not because approvals cannot be answered — they can, the
// driver and the Console card for them are built (P2-5). It is false because in a Workspace
// there is nothing to choose between: measured on 1.3.0-R3401.1, a muse session raises NO
// approval at all, so both values of the choice produce the same ungated session and offering
// it would be a control with no effect.
//
// The chain is short and each link is measured (ADR 0095 P2-6):
//
//   - Gate A: bubblewrap cannot build a sandbox in this container (AppArmor's docker-default
//     refuses mount(2) whatever the capabilities), so `--disable-sandbox` is permanent here.
//   - With that flag the host commits `filesystem.mode: "unrestricted"` with no rules and
//     `local_command_network.mode: "enabled"`; without it, `managed` with six rules and
//     `proxy_only`. `--disable-write`, `--disable-shell` and `--sandbox-network` change
//     NOTHING once `--disable-sandbox` is present (all three measured).
//   - With nothing restricted, every tool call resolves `policy_decision: "allow:policy"`
//     before the approval layer sees it. Measured twice, with real turns: in-workspace
//     `tool:bash` and a `tool:write_file` to `/tmp` — outside `workspaceRoot` entirely —
//     both ran with zero `approval/requested`, under `approvalMode: "onRequest"`.
//
// So decision 5's "approvals stay on" does not hold in a Workspace, and the guide says so.
// `Capabilities.Permissions` stays true because it means something different — the driver
// really does support the approval Interaction kind — and it is read in-process only, so it
// shows the member nothing. This flag is the member-visible one.
//
// CanTranscript is true now that Transcript below really reads a store that the live item
// stream fills.
//
// CanFork and CanForkAt are true together, and for muse they cannot be anything else: there is
// one launch route (managed-only), so `agents.ErrForkAtRoute` — "this kind can fork, but not
// through the route this session took" — has no case here. The whole-conversation fork is the
// point fork with no cut, which is exactly what the wire says (`cutPoint` omitted means "all
// completed turns"), so a kind that could do one and not the other would be AF's invention.
func (agentImpl) Caps() agents.Caps {
	return agents.Caps{
		ManagedOnly:   true,
		CanTranscript: true,
		CanFork:       true,
		CanForkAt:     true,
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
	items, models, err := st.itemsWithModels()
	if err != nil {
		return agents.TranscriptData{}, false
	}
	td := agents.TranscriptData{Turns: turnsFromItems(items), Path: st.Path(), Mode: "normal"}
	// An assistant turn is labelled with the model of its first item. session/setModel takes
	// effect at the next model call, so a turn that spans a switch shows the model it began on.
	for i := range td.Turns {
		if td.Turns[i].Role == "assistant" && td.Turns[i].Model == "" {
			td.Turns[i].Model = models[td.Turns[i].AnchorID]
		}
	}

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

// --- Forker / ForkAtResolver (decision 13: MSP carries `session/fork`) ----------------

// ForkSource returns what the new session's ForkFrom carries: this slot's SID, not the muse
// session id.
//
// That looks like the wrong identifier — `session/fork` takes a muse session id — and it is
// deliberate: the fork has to copy TWO things, the host's conversation and AF's own item store
// (transcript.go's header says why the store exists). The slot sid is the key to both; the
// muse session id is the key to one, and nothing derives the other from it.
func (agentImpl) ForkSource(m session.Meta) (string, error) {
	sid := slotSid(m)
	prev, ok := readSession(sid)
	if !ok || prev.ID == "" {
		return "", errors.New("まだ会話がないためフォークできません")
	}
	return sid, nil
}

// ResolveForkAt turns the clicked anchor into the wire's cut point — a TURN id, where the
// anchor is an ITEM id.
//
// The bridge between the two is the schema's own sentence: an item's `turnId` is "the owning
// turn (== the submitting commandId for fresh turns)", so the item of a user message belongs
// to the turn that message STARTED, not to the one before it. That is what makes the two
// directions simple:
//
//   - Include=false ("redo this message") keeps everything before that turn, so the cut is the
//     PRECEDING turn — and there being none is an error, not a whole-conversation fork.
//   - Include=true ("continue from this message") keeps that turn and its reply, so the cut is
//     the turn itself — unless it is the last one, where "keep everything through the final
//     turn" IS the whole conversation and "" is the value that says so.
//
// `cutPoint.lastTurnId` is INCLUSIVE on the wire, which is why both arms name a turn to keep
// rather than a turn to stop before.
func (agentImpl) ResolveForkAt(m session.Meta, at agents.ForkPoint) (string, error) {
	items, err := openStore(slotSid(m)).Items()
	if err != nil {
		return "", err
	}
	var anchorTurn string
	found := false
	for _, it := range items {
		if it.ItemID != at.Anchor {
			continue
		}
		found = true
		if it.TurnID != nil {
			anchorTurn = *it.TurnID
		}
		break
	}
	if !found {
		return "", fmt.Errorf("フォーク元のターンが見つかりません: %s", at.Anchor)
	}
	if anchorTurn == "" {
		// A `userShell` item is the one kind outside a turn (the schema says so), so there is
		// no boundary to cut at. Refusing is the honest answer; falling back to the whole
		// conversation would look like it worked.
		return "", errors.New("この位置ではフォークできません（ターンに属さない項目です）")
	}
	order := turnOrder(items)
	idx := -1
	for i, id := range order {
		if id == anchorTurn {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", fmt.Errorf("フォーク元のターンが見つかりません: %s", at.Anchor)
	}
	if !at.Include {
		if idx == 0 {
			return "", errors.New("この会話の先頭より前ではフォークできません")
		}
		return order[idx-1], nil
	}
	if idx == len(order)-1 {
		return "", nil // the last exchange: keep the whole conversation
	}
	return order[idx], nil
}
