package main

import (
	"errors"
	"fmt"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// dirGoneErr is the "can't (re)launch — working dir removed" error shared by the
// agents that require their real project dir (claude/opencode/codex).
func dirGoneErr(dir string) error {
	return fmt.Errorf("作業フォルダが存在しないため再開できません: %s", dir)
}

// errSSMNoTarget is returned when an ssm session has no connection target recorded.
var errSSMNoTarget = errors.New("SSM セッションの接続先が指定されていません")

// Coding-agent abstraction. A session's "kind" (claude/opencode/codex/shell/ssm)
// used to be a bare string branched on in ~50 places — a new agent meant touching
// every switch/if. This file makes kind a canonical const list (now
// session.Kind*, internal/session) and folds the per-kind behavior behind an
// Agent interface + registry, so the diverging logic (how to launch the tmux
// program, what live state to surface, which capabilities exist) lives in ONE
// implementation per agent. Adding an agent = one Agent impl + one registry
// entry. The wire contract (session.Session/session.Meta JSON, HTTP shapes) is
// unchanged: the field VALUES are computed exactly as before, only the dispatch moved.

// agentCaps flags the optional features a kind supports, replacing the scattered
// `kind == "claude"` guards at the HTTP endpoints.
type agentCaps struct {
	canFork       bool // POST /fork — copy the conversation into a new session (claude)
	canTranscript bool // GET /output & /messages — read the jsonl transcript (claude)
	usesLabel     bool // set a claude --name display label at create/recreate (claude)
}

// launchOpts carries the per-launch inputs that aren't in session.Meta.
type launchOpts struct {
	ssmForce bool // ssm: force re-login (logout+login) instead of reusing a cached token
}

// launchPlan is what an Agent hands back to startSessionTmux: the pane program and
// the directory to launch it in (which may differ from meta.Dir — shell/ssm fall
// back to home).
type launchPlan struct {
	program string
	cwd     string
}

// liveInfo is the slice of a wire Session whose values depend on the kind and live
// state. wireSession fills the static fields and asks the agent for this.
type liveInfo struct {
	state          string                // claude/opencode/codex live state; "" for shell/ssm
	remoteURL      string                // claude Remote Control URL, "" otherwise
	context        *session.ContextUsage // claude context fill, nil otherwise
	resumable      bool                  // false = stopped agent whose working dir is gone
	backgroundBusy bool                  // claude: idle turn but a run_in_background task lingers
}

// Agent is the per-kind behavior seam. Implementations are stateless value types
// (the registry holds one of each); all session state is derived from the passed
// meta and the on-disk stores.
type Agent interface {
	kind() string
	caps() agentCaps
	// buildLaunch returns the tmux pane program + launch dir for m, or an error when
	// the session can't start (e.g. its working dir is gone). The common tmux
	// plumbing + toolchain prefix is applied by startSessionTmux.
	buildLaunch(m session.Meta, opts launchOpts) (launchPlan, error)
	// wireLive computes the live-dependent Session fields for the sessions list.
	wireLive(m session.Meta, alive bool) liveInfo
	// clearResume forgets any captured per-slot resume id so recreate starts a fresh
	// conversation. No-op for agents that pin their own session id (claude) or keep
	// no resume state (shell/ssm).
	clearResume(sid string)
	// transcript returns the session's full chronological chat turns (normalized to the
	// common transcript.Turn model) plus diagnostics and the reconstructed ToDo list, for agents
	// whose native store isn't claude's <sid>.jsonl (codex rollout, opencode SQLite).
	// ok=false means the agent has no generic transcript source — claude uses its own
	// jsonl path in handleSessionMessages instead. The generic /messages handler windows
	// the turns and surfaces the tasks.
	transcript(m session.Meta) (transcriptData, bool)
}

// transcriptData is what a non-claude agent's transcript() yields: the full
// chronological turns, the source path (diagnostics), and the current ToDo list
// (reconstructed from the agent's plan/todo state; nil when none).
type transcriptData struct {
	turns []transcript.Turn
	path  string
	tasks []transcript.Task
	// mode is the agent's current permission/collaboration mode, normalized to "plan"
	// (plan mode) or "normal", so the Console can show the plan indicator and drive the
	// plan-mode toggle. "" when unknown.
	mode string
	// pending is the question the agent is currently awaiting an answer to (codex
	// request_user_input / opencode question tool), or nil. Surfaced like claude's
	// pending questions so the Console can render it interactively.
	pending []transcript.Question
}

// noGenericTranscript is the transcript() default for agents that either have no
// readable transcript (shell/ssm) or use their own path (claude). Embedding it keeps
// the interface satisfied without a per-agent stub.
type noGenericTranscript struct{}

func (noGenericTranscript) transcript(session.Meta) (transcriptData, bool) {
	return transcriptData{}, false
}

// agents is the kind → Agent registry. agentOf falls back to claude for an unknown
// or empty kind, matching the historical default (a session with no recognized kind
// launches claude).
var agents = map[string]Agent{
	session.KindClaude:   claudeAgent{},
	session.KindOpencode: opencodeAgent{},
	session.KindCodex:    codexAgent{},
	session.KindShell:    shellAgent{},
	session.KindSSM:      ssmAgent{},
}

func agentOf(kind string) Agent {
	if a, ok := agents[kind]; ok {
		return a
	}
	return agents[session.KindClaude]
}

// normalizeKind maps a create request's kind onto a registered one, defaulting the
// unknown/empty/"claude" cases to claude (the historical create whitelist).
func normalizeKind(kind string) string {
	if _, ok := agents[kind]; ok {
		return kind
	}
	return session.KindClaude
}

// --- shared live-state helpers -------------------------------------------------

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
		if st := opencodeLiveState(m); st != "" {
			return st
		}
	}
	sid := session.UUID(m.Dir, m.Name)
	state := status.LiveState(sid)
	if heal && state != "idle" && tmuxx.AtIdlePrompt(m.Name) {
		state = "idle"
		status.Remove(sid)
	}
	return state
}

// statusOnlyLive is the wireLive body shared by opencode/codex: state from the
// status store (no idle-heal, no background-busy), and resumable unless the working
// dir is gone.
func statusOnlyLive(m session.Meta, alive bool) liveInfo {
	li := liveInfo{resumable: true}
	if alive {
		li.state = status.LiveState(session.UUID(m.Dir, m.Name))
	} else if !session.DirExists(m.Dir) {
		li.resumable = false
	}
	return li
}

// --- sid store -----------------------------------------------------------------

// sidStore maps our deterministic slot sid to an agent's own session id, so a slot
// resumes its OWN conversation (internal/fstore の Store に薄い読み口を被せたもの:
// read は ok を潰して "" を返す — 呼び出し側は空文字を「無し」として扱う)。
type sidStore struct{ files fstore.Store[string] }

func (s sidStore) read(sid string) string {
	v, _ := s.files.Read(sid)
	return v
}

func (s sidStore) write(sid, val string) { _ = s.files.Write(sid, val) }
func (s sidStore) remove(sid string)     { s.files.Remove(sid) }

var (
	// opencode: written externally by the bundled plugin (on session.created, keyed
	// by AF_SESSION_SID); the agent only reads/removes it.
	opencodeSids = sidStore{fstore.TrimmedStrings(paths.AgentConfigDir, "opencode-sid")}
	// codex: written by the session-status hook from codex's own session_id (codex
	// has no --session-id flag to pin), read for `codex resume <id>`.
	codexSids = sidStore{fstore.TrimmedStrings(paths.AgentConfigDir, "codex-sid")}
)
