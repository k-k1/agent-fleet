// Package claude holds everything specific to the claude CLI kind: the Agent
// implementation, building the launch command, parsing the jsonl transcript, the
// Connections/Console handlers for auth/settings/usage, the status-hook wiring, the context
// fill and background-work detection. The entry point of the session-status subcommand
// (decoding the hook stdin and applying the pending payload) stays in main.
package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// New returns the claude Agent implementation for the kind registry.
func New() agents.Agent { return agentImpl{} }

// agentImpl is the Agent implementation for the claude kind.
type agentImpl struct{ agents.NoGenericTranscript }

func (agentImpl) Kind() string { return session.KindClaude }

// Caps: claude is the only kind with no official API for branching at a past message
// (docs/log/55), so CanForkAt is served by truncating the transcript jsonl (forkat.go). It
// launches as a TUI only, so unlike the other kinds the capability carries no managed
// condition — whether a launch route can carry a fork point is answered per kind by
// ResolveForkAt.
func (agentImpl) Caps() agents.Caps {
	return agents.Caps{CanFork: true, CanForkAt: true, CanTranscript: true, UsesLabel: true, PermissionChoice: true}
}

// ForkSource resolves this session's conversation id as the fork source, refusing when
// the jsonl holds no real conversation yet — `claude --resume` would die with "No
// conversation found". The id must be the one claude actually writes under (LiveSID),
// not our slot sid: the fork command resumes it verbatim (sid.go).
func (agentImpl) ForkSource(m session.Meta) (string, error) {
	sid := LiveSID(session.UUID(m.Dir, m.Name))
	if !JSONLResumable(sid) {
		return "", errors.New("分岐できる会話がまだありません")
	}
	return sid, nil
}

// ResolveForkAt validates the anchor against this session's own transcript. The value
// travels unchanged: unlike codex, claude's cut is expressed as "the line to stop before",
// which is exactly what the mirror handed out. The work here is refusing the anchors that
// would produce a transcript claude launches but cannot answer in (a tool_use whose
// tool_result fell on the other side of the cut) — see forkat.go for why that matters.
//
// Validating here, at request time, rather than only at launch is deliberate: a refusal
// must reach the user as "this fork point cannot be used" (err.fork_bad_anchor), not as a
// session that starts and dies.
func (agentImpl) ResolveForkAt(m session.Meta, at agents.ForkPoint) (string, error) {
	sid := session.UUID(m.Dir, m.Name)
	lines, path, _ := TranscriptRead(sid)
	if len(lines) == 0 || path == "" {
		return "", errors.New("分岐できる会話がまだありません")
	}
	anchor := at.Anchor
	if at.Include {
		// "continue from this message" = cut just before the NEXT user prompt. The meaning
		// of ForkAt (keep everything up to, not including, this uuid) is unchanged, so both
		// the materialization and the launch pass through as they are.
		next, err := nextPromptUUID(lines, anchor)
		if err != nil {
			return "", err
		}
		if next == "" {
			// The last exchange = keep everything. An empty ForkAt falls to the
			// whole-conversation fork route (--fork-session), which produces the same
			// everything-included result, so it is the right answer here.
			return "", nil
		}
		anchor = next
	}
	// Dry run of the real surgery (destination sid is irrelevant to the checks), so the
	// answer here and the behaviour at launch can never disagree.
	if _, err := buildForkLines(lines, anchor, sid); err != nil {
		return "", err
	}
	return anchor, nil
}

func (agentImpl) BuildLaunch(m session.Meta, _ agents.LaunchOpts) (agents.LaunchPlan, error) {
	// A claude session must launch in its real working dir: if the dir is gone (its
	// repo was deleted) we refuse rather than resume the conversation in an unrelated
	// cwd. wireSession reports this as non-resumable.
	if !session.DirExists(m.Dir) {
		return agents.LaunchPlan{}, agents.DirGoneErr(m.Dir)
	}
	// Pre-trust the launch dir so claude doesn't stall on the folder-trust dialog
	// (not skippable via --dangerously-skip-permissions).
	ensureFolderTrusted(m.Dir)
	sid := session.UUID(m.Dir, m.Name)
	// A jsonl can exist yet hold no real conversation — e.g. only a Remote Control
	// "bridge-session" line when RC connected but nothing was said. claude --resume
	// then dies with "No conversation found". Drop such a stub so buildProgram
	// starts fresh (--session-id) instead of resuming.
	if !JSONLResumable(sid) {
		for _, p := range jsonlPaths(sid) {
			_ = os.Remove(p)
		}
	}
	// First launch of a POINT fork (docs/log/55): write our own truncated transcript before
	// the pane starts. buildProgram then finds a jsonl for sid and resumes it — the fork
	// is invisible from there on, exactly like the whole-conversation fork becomes a
	// plain resume after its first launch. A failure must not fall through to
	// `--fork-session`, which would copy the WHOLE conversation the user asked to cut.
	if m.ForkAt != "" && m.ForkFrom != "" && !SessionJSONLExists(sid) {
		if err := MaterializeForkAt(m.ForkFrom, sid, m.ForkAt); err != nil {
			return agents.LaunchPlan{}, fmt.Errorf("発言時点からの分岐を作成できませんでした: %w", err)
		}
	}
	// No env token is injected: the interactive TUI authenticates from claude's own
	// .credentials.json, written by `claude auth login` via the Connections flow
	// (auth.go). CLAUDE_CODE_OAUTH_TOKEN is headless-only.
	return agents.LaunchPlan{
		Program: buildProgram(sid, m.Model, m.Effort, m.Mode, m.Label, m.ForkFrom, agents.BypassPermissions(m)),
		Cwd:     m.CWD(),
	}, nil
}

func (agentImpl) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	li := agents.LiveInfo{Resumable: true}
	sid := session.UUID(m.Dir, m.Name)
	li.RemoteURL = RemoteSessionURL(sid)
	li.Context = latestContext(sid)
	if alive {
		// Default a live claude with no recorded event yet to idle (it sits at the
		// prompt waiting for input). Hook events refine it.
		//
		// EffectiveModal: AskUserQuestion / ExitPlanMode raise a permission_prompt of their
		// own that overwrites the state with "permission", so the captured payload is taken
		// as the truth. With the bare state, a session showing a question card is the one
		// whose chip claims "waiting for permission" — badge and body disagreeing.
		li.State = status.EffectiveModal(sid, status.LiveState(sid))
		// Every pane-derived verdict is taken from ONE frame, read once (tmuxx.ReadPane).
		// A capture-pane per predicate would cost sessions × poll interval.
		pane := tmuxx.ReadPane(m.Name)
		// The session is pinned on the usage-limit menu waiting for a human
		// (tmuxx.AtRateLimitModal). The turn is already over, yet that menu carries
		// "Esc to cancel" and replaces the composer together with the mode footer, so the
		// AtIdlePrompt self-heal below misses it permanently. Not catching it here leaves the
		// session stuck at "running" forever (measured 2026-07-31: about 16 hours).
		//
		// HealIdle is called only when the state is not idle — the same guard as the self-heal
		// below. MarkTurnEndErr persists idle, so it does not run on later polls (the menu
		// stays up until a human clears it, so without this every poll would fire another
		// notification and completion report).
		//
		// Expired authentication (docs/log/47 §4-8) is checked BEFORE the limit menu because,
		// of the two conditions that can hold at once, it is the one WAITING DOES NOT FIX: a
		// limit lifts when its time comes, an expired credential moves nothing until the user
		// re-authenticates. The menu's automatic release reads the pane directly
		// (rate_limit_resume.go), so answering auth here does not block that recovery path.
		// Folding the turn that is still recorded as running through HealIdle before saying so
		// is the same shape as the limit case: a turn cut off by a 401 fires no Stop hook, and
		// a session stuck at "running" makes the list lie before the badge ever appears.
		if AuthExpired() {
			if li.State != "idle" {
				HealIdle(sid)
			}
			li.State = agents.StateAuth
			return li
		}
		if pane.RateLimitMenu {
			if li.State != "idle" {
				HealIdle(sid)
			}
			li.State = agents.StateBlocked
			return li
		}
		// Self-heal a stale cache: a non-idle state that no longer matches the terminal
		// (killed+resumed, rejected permission, abandoned question) — if the pane is
		// back at the ready prompt, it's idle. HealIdle additionally recognises the one
		// case that IS a real turn end — an API error cut the turn off, so no Stop hook
		// ever fired — and routes it through the notifier instead of silently dropping
		// the completion (docs/log/47).
		//
		// IdleSettled, not Idle: while claude renders an answer it hides the spinner and
		// draws a frame indistinguishable from the ready prompt (measured: 21s per block
		// boundary, byte-identical for up to 11.4s). Healing on that frame badged a
		// session as waiting for input mid-answer — and took the stop button away, fired the
		// completion notification early, and let the idle rules count it as done. The
		// settle window (idlesettle.go) is what tells the two apart.
		if li.State != "idle" && pane.IdleSettled {
			li.State = "idle"
			HealIdle(sid)
		}
		// Idle by hook, but the pane may actually be mid-turn: the "working" status file
		// can go missing (never written, or removed by the self-heal above during a
		// transient prompt frame) and no mid-turn hook rewrites it in bypass mode, so a
		// busy session would wrongly read idle. IsBusy trusts the live TUI (interrupt
		// affordance shown) and persists working — self-limiting to one capture per turn,
		// since the next poll then reads "working" from the file.
		if li.State == "idle" && pane.Busy {
			li.State = "working"
			status.Persist(sid, "working")
		}
		// Still idle: background work may yet be running — surface it so "waiting for input"
		// isn't mistaken for "done".
		if li.State == "idle" {
			li.BackgroundBusy, li.BackgroundBusyReason = BackgroundWork(m.Name, sid)
		}
	} else if !session.DirExists(m.Dir) {
		// A stopped claude whose working dir was removed (its repo deleted) can't be
		// resumed there; the Console marks it non-resumable (archive only).
		li.Resumable = false
	}
	return li
}

func (agentImpl) ClearResume(string) {}

// RemoteSessionURL derives the claude.ai Remote Control page for sid from its
// jsonl "bridge-session" line (written when RC connects). The web URL is
// "…/code/session_<bridgeSessionId without the cse_ prefix>". We read only the
// head of the log (the bridge line is written at session start) to stay cheap on
// the polled list. Returns "" when there is no bridge (RC off / not yet connected).
func RemoteSessionURL(sid string) string {
	for _, p := range jsonlPaths(sid) {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		buf := make([]byte, 64*1024)
		n, _ := f.Read(buf)
		f.Close()
		for _, line := range strings.Split(string(buf[:n]), "\n") {
			if !strings.Contains(line, `"type":"bridge-session"`) {
				continue
			}
			var b struct {
				BridgeSessionID string `json:"bridgeSessionId"`
			}
			if json.Unmarshal([]byte(line), &b) == nil && b.BridgeSessionID != "" {
				return "https://claude.ai/code/session_" + strings.TrimPrefix(b.BridgeSessionID, "cse_")
			}
		}
	}
	return ""
}
