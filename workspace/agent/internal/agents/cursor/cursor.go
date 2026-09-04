// Package cursor is the vertical slice for the Cursor CLI (`cursor-agent` / Anysphere) kind
// (docs/log/40 Track A). It keeps the read layer — the Agent implementation and the
// transcript/state reads over Claude Code-compatible JSONL — inside the kind; the managed
// driver (`cursor-agent agent acp`: a per-session child speaking ACP JSON-RPC over stdio)
// lives in driver.go/serve.go.
//
// Session identity is a v4 UUID minted by AF and handed over as `--resume <uuid>`
// (measured: an unknown valid v4 creates a chat, an existing one resumes it) — the same
// shape as copilot's --session-id, so agy's "cannot obtain a resume UUID" problem
// (docs/log/32 202e439) cannot arise here by construction. The authoritative read source is
// the Claude Code-compatible JSONL transcript (program.go transcriptPath), never the private
// SQLite store (~/.cursor/chats/**/store.db): a change to opencode's store contract once
// produced a false idle (docs/log/40 decision 3). Auth has its own flow, with credentials in
// ~/.config/cursor/auth.json (auth.go).
package cursor

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// sids maps our deterministic slot sid to the cursor chat UUID. Written at FRESH
// launch time with an AF-generated v4 UUID — cursor accepts `--resume <uuid>` to
// CREATE a chat under that id (measured) and resumes it later, so there is no capture
// race to solve.
var sids = agents.NewSidStore("cursor-sid")

// New returns the cursor Agent implementation for the kind registry.
func New() agents.Agent { return agentImpl{} }

type agentImpl struct{}

func (agentImpl) Kind() string { return session.KindCursor }

// No fork (cursor's `/fork` is TUI-only) and no display label. The chat mirror IS
// supported: transcript.go reads the Claude Code-compatible JSONL, which cursor
// appends live in the TUI/-p routes.
func (agentImpl) Caps() agents.Caps {
	return agents.Caps{CanTranscript: true, PermissionChoice: true}
}

func (agentImpl) BuildLaunch(m session.Meta, _ agents.LaunchOpts) (agents.LaunchPlan, error) {
	if !session.DirExists(m.Dir) {
		return agents.LaunchPlan{}, agents.DirGoneErr(m.Dir)
	}
	// If cursor stopped using the id we pushed on it, pick the current one up before
	// launching (sid.go). Without that repair we keep passing `--resume <unused id>` and
	// the user's conversation is left behind with nothing referencing it.
	chatID := resolveSid(m)
	if chatID == "" {
		var err error
		if chatID, err = newChatID(); err != nil {
			return agents.LaunchPlan{}, fmt.Errorf("チャット ID を採番できません: %w", err)
		}
		sids.Write(session.UUID(m.Dir, m.Name), chatID)
	}
	return agents.LaunchPlan{Program: buildProgram(m.Model, m.Mode, chatID, agents.BypassPermissions(m)), Cwd: m.CWD()}, nil
}

func (agentImpl) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	// The cursor TUI state is classified from the tail of the JSONL transcript (state.go):
	// no dependence on TUI strings, which is what the false-idle lesson asks for. The
	// managed (ACP) route writes no transcript, so there the driver's runTurn boundaries
	// are the state source.
	li := agents.LiveInfo{Resumable: true}
	if alive {
		// Liveness polling is where drift gets noticed (cursor has no hooks). resolveSid
		// repairs the ledger, so later ChatID reads point at the new conversation (sid.go).
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

// PendingModal hands the modal that is waiting on a human to the carry-over, just before
// the session is folded (docs/log/75 P5).
//
// For cursor the only such wait is ACP's `session/request_permission` (plan launches, or
// when bypass is turned off). The TUI route's approval menu leaves no trace in the JSONL
// (see the comment at the top of state.go) and cannot be observed — report what cannot be
// obtained as absent.
//
// Kind is permission. The Interaction itself calls itself a "question", but that is only the
// shape that makes the Console draw a choice card; the answer's destination is the ACP
// JSON-RPC id. Once the child process is gone, letting the user pick yes or no delivers
// nothing (docs/log/75 §75.6.4), so all that is carried over is the fact of what was asked.
//
// The handle exists only in memory: unless this is called before the session is folded,
// nothing survives. Promotion is triggered from halt and gracefulShutdown; only a SIGKILL
// of the whole container escapes it.
func (agentImpl) PendingModal(m session.Meta) (agents.PendingModal, bool) {
	if m.DriverKind() != session.DriverManaged {
		return agents.PendingModal{}, false
	}
	h := handleFor(m.Name)
	if h == nil {
		return agents.PendingModal{}, false
	}
	detail := h.pendingPermission()
	if detail == "" {
		return agents.PendingModal{}, false
	}
	return agents.PendingModal{Kind: "permission", Detail: detail}, true
}

func (agentImpl) ClearResume(sid string) { sids.Remove(sid) }

// ChatID returns the slot's cursor chat UUID ("" when none allocated yet).
func ChatID(m session.Meta) string { return sids.Read(session.UUID(m.Dir, m.Name)) }

// newChatID generates an RFC4122 v4 UUID. cursor's --resume accepts a self-minted
// valid v4 to create a fresh chat (measured); the version/variant bits keep it a
// well-formed UUID so the CLI doesn't reject it.
func newChatID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}
