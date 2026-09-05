// Package copilot is the vertical package for the GitHub Copilot CLI kind (`copilot`, npm
// @github/copilot; docs/log/36 Track A). It keeps inside the kind both the read layer (the
// Agent implementation, the transcript and state read from events.jsonl) and the managed
// driver (--acp: Agent Client Protocol JSON-RPC over stdio, a per-session child -
// driver.go/acp.go).
//
// Session identity is a UUID minted by AF (`--session-id <uuid v4>`, shared by the TUI and by
// ACP's session/load), so agy's "the resume UUID cannot be obtained" problem (docs/log/32
// 202e439) cannot arise here by construction. The read source of truth is
// $COPILOT_HOME/session-state/<sid>/events.jsonl - one format across the TUI, -p and ACP,
// appended live (measured in docs/log/36). Auth rides on the GitHub integration (the OAuth
// token of gh's transparent auth; the Copilot CLI officially supports COPILOT_GITHUB_TOKEN >
// GH_TOKEN > GITHUB_TOKEN and the gh CLI app's token), so there is no Connections flow of its
// own.
package copilot

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// sids maps our deterministic slot sid to the copilot session UUID. Written at
// FRESH launch time (BuildLaunch / driver Resume) with an AF-generated v4 UUID —
// copilot accepts `--session-id <uuid>` to CREATE a session under that id and
// `session/load` resumes it, so there is no capture race to solve.
var sids = agents.NewSidStore("copilot-sid")

// New returns the copilot Agent implementation for the kind registry.
func New() agents.Agent { return agentImpl{} }

type agentImpl struct{}

func (agentImpl) Kind() string { return session.KindCopilot }

// The chat mirror IS supported: transcript.go reads session-state/<sid>/events.jsonl,
// which copilot appends live in every mode. CanFork/CanForkAt: copilot exposes no fork
// affordance of its own, so both are done by copying the session-state directory and
// truncating events.jsonl (forkat.go) - events.jsonl is the restore source (measured,
// docs/log/55 §55.5). No display label.
func (agentImpl) Caps() agents.Caps {
	return agents.Caps{CanTranscript: true, CanFork: true, CanForkAt: true, PermissionChoice: true}
}

// ForkSource resolves this session's copilot session id as the fork source, refusing when
// nothing has been recorded yet (a branch of an empty conversation is a new session).
func (agentImpl) ForkSource(m session.Meta) (string, error) {
	sid := SessionID(m)
	if sid == "" {
		return "", errors.New("分岐できる会話がまだありません")
	}
	lines, err := readEventLines(sid)
	if err != nil || !hasUserMessage(lines) {
		return "", errors.New("分岐できる会話がまだありません")
	}
	return sid, nil
}

// ResolveForkAt validates the anchor against this session's own events.jsonl. Like
// claude, the value travels unchanged (the cut is expressed as "stop before this event"),
// and validating here rather than only at launch means a bad anchor is reported as such
// instead of as a session that starts and dies.
func (agentImpl) ResolveForkAt(m session.Meta, at agents.ForkPoint) (string, error) {
	sid := SessionID(m)
	if sid == "" {
		return "", errors.New("分岐できる会話がまだありません")
	}
	lines, err := readEventLines(sid)
	if err != nil {
		return "", errors.New("copilot の会話ログを読めません")
	}
	anchor := at.Anchor
	if at.Include {
		next, err := nextPromptID(lines, anchor)
		if err != nil {
			return "", err
		}
		if next == "" {
			return "", nil // the last exchange = keep everything (falls to the whole-conversation path)
		}
		anchor = next
	}
	// Dry run of the real surgery so this answer and the launch can never disagree.
	if _, err := forkEventLines(lines, anchor, sid, sid); err != nil {
		return "", err
	}
	return anchor, nil
}

func (agentImpl) BuildLaunch(m session.Meta, _ agents.LaunchOpts) (agents.LaunchPlan, error) {
	if !session.DirExists(m.Dir) {
		return agents.LaunchPlan{}, agents.DirGoneErr(m.Dir)
	}
	// Pre-trust the launch dir so the TUI doesn't stall on its "Confirm folder trust" dialog
	// (measured: appending to config.json trustedFolders beforehand skips it). Re-applied on
	// every launch - the agy 00dacc5 lesson that a one-time fix peels off later.
	EnsureFolderTrusted(m.Dir)
	// If copilot stopped using the id we imposed, pick the real one up before launching
	// (sid.go). Without that fix here we keep passing `--session-id <an id nobody uses>` and
	// the user's conversation is left behind, referenced from nowhere.
	sid := resolveSid(m)
	if sid == "" {
		var err error
		if sid, err = newSessionID(); err != nil {
			return agents.LaunchPlan{}, fmt.Errorf("セッション ID を採番できません: %w", err)
		}
		sids.Write(session.UUID(m.Dir, m.Name), sid)
	}
	// First launch of a fork (docs/log/55): build our own session-state directory before
	// copilot starts, so the launch below is an ordinary `--session-id <sid>` resume.
	// A failure must NOT fall through to a fresh session — the user asked for a branch
	// carrying history, and starting empty looks like the branch silently lost it.
	if m.ForkFrom != "" && !sessionStateExists(sid) {
		if err := MaterializeForkAt(m.ForkFrom, sid, m.ForkAt); err != nil {
			return agents.LaunchPlan{}, fmt.Errorf("分岐を作成できませんでした: %w", err)
		}
	}
	return agents.LaunchPlan{Program: buildProgram(m.Model, m.Effort, m.Mode, sid, agents.BypassPermissions(m)), Cwd: m.CWD()}, nil
}

func (agentImpl) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	// copilot has no status hooks; working/idle/question is derived from the events.jsonl
	// tail (state.go), independent of TUI strings as the false-idle lesson requires.
	li := agents.LiveInfo{Resumable: true}
	if alive {
		// The liveness poll is where drift is detected (copilot has no hook). resolveSid
		// repairs the ledger, so later SessionID reads point at the new conversation (sid.go).
		resolveSid(m)
		if st := LiveState(m); st != "" {
			li.State = st
		}
	}
	if !alive && !session.DirExists(m.Dir) {
		li.Resumable = false
	}
	return li
}

// PendingModal hands the human-wait that existed just before shutdown to the carry-over
// (docs/log/75 P5).
//
// copilot only ever waits on a permission request, and both the TUI route and the managed (ACP)
// one record `permission.requested` in the SAME events.jsonl (state.go), so one read covers
// both. Being a file, it survives the process, and can still be picked up later than halt.
//
// Kind is permission: the destination for the answer (the TUI menu, the ACP JSON-RPC id) dies
// with the process, so only the fact carries over (docs/log/75 §75.6.4). The subject may be
// unreadable depending on the events.jsonl schema, and then the card states the fact alone.
func (agentImpl) PendingModal(m session.Meta) (agents.PendingModal, bool) {
	detail, pending := PendingPermission(m)
	if !pending {
		return agents.PendingModal{}, false
	}
	return agents.PendingModal{Kind: "permission", Detail: detail}, true
}

func (agentImpl) ClearResume(sid string) { sids.Remove(sid) }

// SessionID returns the slot's copilot session UUID ("" when none allocated yet).
func SessionID(m session.Meta) string { return sids.Read(session.UUID(m.Dir, m.Name)) }

// newSessionID generates an RFC4122 v4 UUID. copilot VALIDATES the version/variant bits of
// --session-id (measured: a non-v4 value fails to launch with "The value is not a valid UUID").
func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}
