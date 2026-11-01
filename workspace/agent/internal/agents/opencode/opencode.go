// Package opencode is the vertical slice for the opencode CLI kind (docs/log/23 remaining
// item 1, Wave D: the first CLI slice). It holds the Agent implementation, launch-command
// assembly, the SQLite transcript reader, the Connections handler for provider keys, and
// rtk plugin application, all moved out of package main. Behaviour, wire and on-disk state
// must stay byte-identical to what main produced.
package opencode

import (
	"errors"
	"fmt"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// SlotSessionID is the opencode session id ("ses_…") slot m is mapped to, "" when it has
// none yet. The af MCP server matches the id the caller plugin stamps on a tools/call
// against it (#989), so AF's own mapping — never the caller's claim — decides ownership.
func SlotSessionID(m session.Meta) string { return sids.Read(session.UUID(m.Dir, m.Name)) }

// New returns the opencode Agent implementation for the kind registry.
func New() agents.Agent { return agentImpl{} }

// agentImpl is the Agent implementation for the opencode kind (docs/log/23 P1 remaining:
// splitting the CLI slices into their own files).
type agentImpl struct{}

func (agentImpl) Kind() string { return session.KindOpencode }

// CanTranscript lights up the Console chat mirror for opencode; its turns come from the
// SQLite store via Transcript() (readTranscript), windowed by the generic /messages
// handler. CanFork: the conversation forks via `opencode --session <src> --fork`
// (ForkSource / BuildLaunch), aligning the fork affordance with claude. CanForkAt: the
// fork can also be pinned to a past turn — the serve API's `POST /session/{id}/fork`
// takes a messageID (docs/log/55). That route only exists on the managed driver, so the
// handler refuses a point fork for a CLI-route session rather than silently widening it.
func (agentImpl) Caps() agents.Caps {
	return agents.Caps{CanTranscript: true, CanFork: true, CanForkAt: true, ManagedDefault: true, MCPClient: agents.TriYes}
}

// ForkAtOnTerminal reports whether a Terminal session of this kind can fork at a past message.
func (agentImpl) ForkAtOnTerminal(m session.Meta) bool { return forkAtOnSharedTerminal(m) }

// forkAtOnSharedTerminal is whether a point fork of Terminal session m can reach the serve API:
// its pane is attached to af's serve (sharedSlot) AND the fork's own launch will use the serve
// too (sharedTUI). Either alone is not enough: a standalone pane has no API to read the
// conversation through, and a launch that falls back to --standalone cannot cut at a point, so
// BuildLaunch refuses it rather than copy the whole conversation.
func forkAtOnSharedTerminal(m session.Meta) bool {
	return sharedSlot(m) && sharedTUI()
}

// ForkSource resolves this session's current opencode conversation as the fork source.
func (agentImpl) ForkSource(m session.Meta) (string, error) {
	info, err := v2TargetFor(m).v2Session(m)
	if err != nil {
		return "", errors.New("opencode の会話ストアを読めません")
	}
	if info == nil {
		return "", errors.New("分岐できる会話がまだありません")
	}
	return info.ID, nil
}

// ResolveForkAt validates a mirror anchor against this session's OWN conversation.
// opencode's fork endpoint takes the messageID as-is and stops copying at the first
// message that sorts >= it (docs/log/55 §55.2), so the anchor needs no translation — the work
// here is refusing the anchors that would branch something other than what was pointed at.
func (a agentImpl) ResolveForkAt(m session.Meta, at agents.ForkPoint) (string, error) {
	point, _, err := a.ResolveForkAtPinned(m, at)
	return point, err
}

// ResolveForkAtPinned is ResolveForkAt plus the conversation the point was resolved against
// ("" when the route does not name it).
func (agentImpl) ResolveForkAtPinned(m session.Meta, at agents.ForkPoint) (string, string, error) {
	anchor := at.Anchor
	if anchor == "" {
		return "", "", errors.New("分岐点が指定されていません")
	}
	// Only the serve API takes a fork point; `--session <src> --fork` has no such argument.
	// A managed session talks to it directly and a Terminal pane attached to af's shared serve
	// forks through the same endpoint (buildSharedLaunch).
	if m.DriverKind() != session.DriverManaged && !forkAtOnSharedTerminal(m) {
		return "", "", fmt.Errorf("%w: opencode の発言時点からの分岐は managed のセッション、"+
			"または opencode 2 の共有サーバに接続したターミナルでのみ利用できます",
			agents.ErrForkAtRoute)
	}
	return resolveForkAtV2(v2TargetFor(m), m, at)
}

func (agentImpl) Transcript(m session.Meta) (agents.TranscriptData, bool) {
	return readTranscript(m)
}

// UsageTurns implements agents.UsageReader.
func (agentImpl) UsageTurns(m session.Meta) []transcript.Turn { return usageTurns(m) }

// PendingModal hands the wait-for-a-human state that existed just before folding up over
// to the carry-forward (docs/log/75 P5).
//
// For opencode that state is the question tool and nothing else. Permission
// (`permission.asked`) is auto-allowed unconditionally by the managed driver, so a
// permission prompt a human answers does not exist (the reasoning is spelled out in
// docs/log/75 §75.7 P5).
//
// The pending state lives in opencode's own store (a running question tool in the part
// table) and survives the process dying, so a trigger later than halt can still pick it up.
func (a agentImpl) PendingModal(m session.Meta) (agents.PendingModal, bool) {
	td, _ := a.Transcript(m)
	if len(td.Pending) == 0 {
		return agents.PendingModal{}, false
	}
	return agents.PendingModal{Kind: "question", Questions: td.Pending}, true
}

func (agentImpl) BuildLaunch(m session.Meta, _ agents.LaunchOpts) (agents.LaunchPlan, error) {
	// opencode resumes (or starts) in its real project dir; refuse if it's gone.
	if !session.DirExists(m.Dir) {
		return agents.LaunchPlan{}, agents.DirGoneErr(m.Dir)
	}
	// AF_SESSION_SID lets the bundled opencode plugin report this session's
	// working/idle state back keyed by OUR deterministic sid (same store claude
	// uses), so wireSession can surface it. Provider API keys are injected as env
	// (ANTHROPIC_API_KEY, …) so opencode authenticates without a plaintext file. The
	// env rides LaunchPlan.Env → `tmux new-session -e` (reaches the pane process;
	// verified on tmux 3.3a) so the keys never appear in the command string /
	// /proc/*/cmdline / pane_start_command.
	ocSid := session.UUID(m.Dir, m.Name)
	spec := CurrentLaunch()
	if err := PrepareStart(spec); err != nil {
		return agents.LaunchPlan{}, err
	}
	// The fleet's own inference engines (ADR 0071): env() already carries a workspace-scoped
	// engine token (it has to, for the managed route's shared daemon), and this route can do
	// better — a tmux session is its own process, so it gets a token scoped to THIS session,
	// which is what makes the usage row say who spent it. Merged rather than appended: two
	// entries of the same name in `tmux new-session -e` is not something to leave to luck.
	envs := mergeCommandEnv(append([]string{"AF_SESSION_SID=" + ocSid}, spec.Env()...), EngineEnv(m.Name))
	// The TUI attaches to af's own serve. The session is created (or resumed, or forked) through
	// its API here, so none of the store reads below apply to it.
	if sharedTUI() {
		plan, ok, err := buildSharedLaunch(spec, m, ocSid)
		if err != nil {
			return agents.LaunchPlan{}, err
		}
		if ok {
			return agents.LaunchPlan{Program: plan.Program, Cwd: m.CWD(), Env: mergeCommandEnv(envs, plan.Env)}, nil
		}
	} else {
		markSlotMode(ocSid, modeStandalone)
	}
	// Resume the slot's OWN opencode conversation: the plugin-captured per-slot id, else a
	// conversation in the store this slot itself opened — never an older one from the same dir.
	resume := ""
	if info, err := storeSession(m); err == nil && info != nil {
		resume = info.ID
	} else if err != nil {
		resume = sids.Read(ocSid) // store unreadable — the captured mapping is all we have
	}
	// First launch of a forked slot: no own conversation yet — copy the source and
	// diverge (`--session <src> --fork`). opencode materializes the fork as a NEW
	// session at boot; the plugin records its id for this slot, so later launches
	// resume the fork normally (ForkFrom is then ignored, like claude's).
	fork := false
	if resume == "" && m.ForkFrom != "" {
		// `--session <src> --fork` has no "fork up to here" argument — only the serve API
		// does (docs/log/55 §55.5). Refuse rather than launch a CLI fork that would quietly
		// copy the WHOLE conversation when the user asked for a point.
		if m.ForkAt != "" {
			return agents.LaunchPlan{}, errors.New(
				"発言時点からの分岐は managed のセッションでのみ利用できます")
		}
		resume, fork = m.ForkFrom, true
	}
	return agents.LaunchPlan{Program: buildProgramV2(spec, m.Model, m.Mode, resume, fork), Cwd: m.CWD(), Env: envs}, nil
}

func (agentImpl) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	// State comes from the conversation's reader (LiveState) — robust against the status plugin
	// not firing — falling back to the plugin status file when it cannot say (a --standalone pane,
	// or a daemon that does not answer). Resumable unless the working dir is gone.
	li := agents.LiveInfo{Resumable: true}
	if alive {
		if st := LiveState(m); st != "" {
			li.State = st
		} else {
			li.State = status.LiveState(session.UUID(m.Dir, m.Name))
		}
		// Same vocabulary as codex: a turn busy compacting its context shows as such, and
		// counts as busy everywhere "working" does.
		if li.State == "working" && isCompacting(m) {
			li.State = "compacting"
		}
	} else if !session.DirExists(m.Dir) {
		li.Resumable = false
	}
	return li
}

func (agentImpl) ClearResume(sid string) { sids.Remove(sid) }

var _ agents.UsageReader = agentImpl{}
