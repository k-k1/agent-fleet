// Package codex is the vertical slice for the codex CLI kind (docs/log/23 remaining item 1,
// Wave E): the Agent implementation, launch command assembly, rollout JSONL transcript
// reading, the auth/usage Connections handlers and rtk block application.
//
// Behaviour, wire format and on-disk state must stay byte-identical to what package main
// produced.
package codex

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// sids maps our deterministic slot sid to codex's own session id: written by the
// session-status hook (RememberSid — codex has no --session-id flag to pin), read
// for `codex resume <id>`.
var sids = agents.NewSidStore("codex-sid")

// compacting is keyed by Codex's native thread id and fed by app-server
// contextCompaction item lifecycle events (see package main/codex_appserver.go).
var compacting sync.Map

func SetCompacting(threadID string, active bool) {
	if threadID == "" {
		return
	}
	if active {
		compacting.Store(threadID, true)
	} else {
		compacting.Delete(threadID)
	}
}

func ClearCompacting() { compacting.Range(func(k, _ any) bool { compacting.Delete(k); return true }) }

func IsCompactingThread(threadID string) bool {
	_, ok := compacting.Load(threadID)
	return ok
}

func isCompacting(m session.Meta) bool {
	threadID := sids.Read(session.UUID(m.Dir, m.Name))
	return IsCompactingThread(threadID)
}

// RememberSid records the slot sid → codex session id mapping. Called from the
// session-status hook entrypoint in package main when codex's hook JSON carries
// its own session_id.
func RememberSid(slotSid, codexID string) { sids.Write(slotSid, codexID) }

// New returns the codex Agent implementation for the kind registry.
func New() agents.Agent { return agentImpl{} }

// agentImpl is the Agent implementation for the codex kind.
type agentImpl struct{}

func (agentImpl) Kind() string { return session.KindCodex }

// CanTranscript lights up the Console chat mirror for codex; its turns come from the
// rollout JSONL via Transcript() (readTranscript), windowed by the generic /messages
// handler. CanFork: the conversation forks via `codex fork <id>` (ForkSource /
// BuildLaunch). No label (codex has no --name). CanForkAt: the fork can also be pinned to
// a past turn via `thread/fork`'s lastTurnId (docs/log/55) — app-server only, so the handler
// refuses a point fork for a CLI-route session (`codex fork <id>` has no such argument).
func (agentImpl) Caps() agents.Caps {
	return agents.Caps{CanTranscript: true, CanFork: true, CanForkAt: true}
}

// ForkSource resolves this session's codex conversation id as the fork source —
// the hook-captured per-slot id, provided its rollout actually exists on disk.
func (agentImpl) ForkSource(m session.Meta) (string, error) {
	id := sids.Read(session.UUID(m.Dir, m.Name))
	if id == "" || rolloutPath(id) == "" {
		return "", errors.New("分岐できる会話がまだありません")
	}
	return id, nil
}

// ResolveForkAt translates a mirror anchor into codex's lastTurnId.
//
// This is the one kind where the anchor is NOT what gets sent. The Console's meaning is
// exclusive ("branch before this turn"); `thread/fork`'s lastTurnId is **inclusive**
// ("fork through this turn"), so the answer is the turn BEFORE the anchor. Passing the
// anchor itself would carry the very prompt the user wanted to retake — and the branch
// would look correct in the mirror, just one exchange too long.
//
// Branching before the FIRST turn has no representable lastTurnId: an empty value means
// "the whole conversation" to codex, which is the opposite. Refuse instead of sending it.
func (agentImpl) ResolveForkAt(m session.Meta, at agents.ForkPoint) (string, error) {
	anchor := at.Anchor
	if anchor == "" {
		return "", errors.New("分岐点が指定されていません")
	}
	// Only the app-server takes a fork point; `codex fork <id>` has no such argument.
	if m.DriverKind() != session.DriverManaged {
		return "", fmt.Errorf("%w: codex の発言時点からの分岐は managed のセッションでのみ利用できます",
			agents.ErrForkAtRoute)
	}
	id := sids.Read(session.UUID(m.Dir, m.Name))
	path := rolloutPath(id)
	if id == "" || path == "" {
		return "", errors.New("分岐できる会話がまだありません")
	}
	lines, err := rolloutLines(path)
	if err != nil {
		return "", errors.New("codex の会話ログを読めません")
	}
	ids := rolloutTurnIDs(lines)
	for i, tid := range ids {
		if tid != anchor {
			continue
		}
		// The inclusive case is straightforward here: lastTurnId is inclusive, so passing the
		// turn itself keeps that exchange. Only the exclusive side needs the shift back by one.
		if at.Include {
			return tid, nil
		}
		if i == 0 {
			return "", errors.New("最初のやり取りからは分岐できません（新しいセッションを作ってください）")
		}
		return ids[i-1], nil
	}
	return "", errors.New("指定された分岐点がこの会話に見つかりません")
}

func (agentImpl) Transcript(m session.Meta) (agents.TranscriptData, bool) {
	return readTranscript(m)
}

// PendingModal hands the modal that is waiting on a human to the carry-over, just before
// the session is folded (docs/log/75 P5).
//
// For codex the only such wait is `request_user_input` (a question). Tool approval never
// reaches a human: under managed, appclient.go auto-answers the app-server's
// `item/permissions/requestApproval`, and the TUI route launches in bypass mode so no
// approval prompt appears at all. So permission is never returned (docs/log/75 §75.7 P5).
//
// Where the pending state lives differs between TUI (an unanswered function_call at the
// tail of the rollout) and managed (the handle's Interaction), but readTranscript folds
// both into Pending via managedEnrich, so reading that one field is enough.
func (a agentImpl) PendingModal(m session.Meta) (agents.PendingModal, bool) {
	td, _ := a.Transcript(m)
	if len(td.Pending) == 0 {
		return agents.PendingModal{}, false
	}
	return agents.PendingModal{Kind: "question", Questions: td.Pending}, true
}

func (agentImpl) BuildLaunch(m session.Meta, _ agents.LaunchOpts) (agents.LaunchPlan, error) {
	// codex resumes (or starts) in its real project dir; refuse if it's gone.
	if !session.DirExists(m.Dir) {
		return agents.LaunchPlan{}, agents.DirGoneErr(m.Dir)
	}
	// Pre-accept codex's per-dir trust gate so a freshly cloned repo doesn't stall at
	// the "Do you trust this directory?" prompt (the bypass flags don't cover it).
	ensureFolderTrusted(m.Dir)
	// The shared app-server starts on demand, and the TUI route is one of those demands:
	// without waking it here buildProgram finds no marker (env), launches directly without
	// --remote, and compaction detection, live rate limits and reroute observation
	// (docs/log/27 P1) all disappear. Failure is not fatal — fall back to a direct launch.
	if _, _, err := Serve().Ensure(); err != nil {
		log.Printf("codex app-server unavailable; using direct TUI: %v", err)
	}
	// Auth is codex's own ~/.codex/auth.json (codex login, written via the Connections
	// flow), so no token is injected. State + per-slot resume are wired purely through
	// codex hooks injected on the command line (-c), keyed by our deterministic slot
	// sid — see buildProgram.
	cxSid := session.UUID(m.Dir, m.Name)
	// First launch of a forked slot: no own captured session yet — fork the source
	// conversation (`codex fork <id>`). The injected hooks record the fork's own id
	// on the first prompt, so later launches resume it and ForkFrom is ignored.
	forkFrom := ""
	if sids.Read(cxSid) == "" {
		forkFrom = m.ForkFrom
	}
	// `codex fork <id>` takes no fork point — only the app-server's thread/fork does
	// (docs/log/55 §55.5). Refuse rather than launch a CLI fork that would quietly copy the
	// WHOLE conversation when the user asked for a point. The handler gates on managed
	// first; this is the second line.
	if forkFrom != "" && m.ForkAt != "" {
		return agents.LaunchPlan{}, errors.New("発言時点からの分岐は managed のセッションでのみ利用できます")
	}
	return agents.LaunchPlan{Program: buildProgram(m.Model, m.Effort, cxSid, sids.Read(cxSid), forkFrom), Cwd: m.CWD()}, nil
}

func (agentImpl) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	// State comes from codex's -c-injected status hooks keyed by our sid (the status
	// store; no idle-heal, no background-busy). Resumable unless the working dir is gone.
	// Under managed (docs/log/27 P3) there are no hooks; instead the driver writes the turn
	// boundaries (turn/started, turn/completed notifications) to the same status store, so
	// the reading side is almost entirely shared.
	li := agents.LiveInfo{Resumable: true}
	if alive {
		sid := session.UUID(m.Dir, m.Name)
		st, hasStatus := status.Read(sid)
		li.State = "idle"
		if hasStatus {
			li.State = st.State
		}
		// A missed Stop hook otherwise leaves Codex "working" forever even after its
		// TUI has returned to the composer. Heal it from the rollout's independent
		// task_complete event, but only when it belongs to this working interval.
		// (Managed is event-driven and should not need this, but shares it as a
		// harmless safety net.)
		if li.State == "working" {
			workingSince, _ := time.Parse(time.RFC3339, st.TS)
			if rolloutCompletedAfter(m, workingSince) {
				li.State = "idle"
			}
		}
		if isCompacting(m) {
			li.State = "compacting"
		}
		if m.DriverKind() == session.DriverManaged {
			// Under managed the handle's Interaction is authoritative for questions (it
			// recovers through redelivery of the server request, §12.3) — more accurate
			// and cheaper than probing the rollout tail.
			if h := handleFor(m.Name); h != nil && h.hasQuestion() {
				li.State = "question"
			}
			// A managed session whose turn failed on a usage limit looks like it is waiting
			// for input, but resending gets the same result, so show "limited" instead.
			// StateLimited rather than blocked (claude's limit menu, which stalls until a
			// human picks in the pane) because codex has no menu to dismiss and the window
			// simply reopens with time — the next action to suggest differs. turnError is
			// cleared when the next turn starts, so this never sticks forever.
			if li.State == "idle" && IsRateLimited(m.Name) {
				li.State = agents.StateLimited
			}
		} else if li.State == "working" && HasPendingQuestion(m) {
			// The hooks report only working/idle — a request_user_input dialog keeps
			// the turn "working" forever. Probe the rollout tail so the sessions list
			// shows a pending question (and notifies) like claude; only while working, to keep
			// the probe off the common idle path.
			li.State = "question"
		}
	} else if !session.DirExists(m.Dir) {
		li.Resumable = false
	}
	return li
}

func (agentImpl) ClearResume(sid string) { sids.Remove(sid) }

// IsRateLimited reports whether a managed codex session's last turn failed with a
// usage-limit error. The shared live-state helper in package main uses this to
// surface "blocked" in the sessions list.
func IsRateLimited(name string) bool {
	h := handleFor(name)
	if h == nil {
		return false
	}
	ce := h.turnError()
	return ce != nil && ce.label == "usageLimitExceeded"
}
