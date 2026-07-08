package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
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
// every switch/if. This file makes kind a canonical const list and folds the
// per-kind behavior behind an Agent interface + registry, so the diverging logic
// (how to launch the tmux program, what live state to surface, which capabilities
// exist) lives in ONE implementation per agent. Adding an agent = one Agent impl +
// one registry entry. The wire contract (Session/sessionMeta JSON, HTTP shapes) is
// unchanged: the field VALUES are computed exactly as before, only the dispatch moved.

// Canonical kind list. These are the persisted sessionMeta.Kind / wire Session.Kind
// values; keep them in sync with the registry below and the Console's Session type.
const (
	kindClaude   = "claude"
	kindOpencode = "opencode"
	kindCodex    = "codex"
	kindShell    = "shell"
	kindSSM      = "ssm"
)

// agentCaps flags the optional features a kind supports, replacing the scattered
// `kind == "claude"` guards at the HTTP endpoints.
type agentCaps struct {
	canFork       bool // POST /fork — copy the conversation into a new session (claude)
	canTranscript bool // GET /output & /messages — read the jsonl transcript (claude)
	usesLabel     bool // set a claude --name display label at create/recreate (claude)
}

// launchOpts carries the per-launch inputs that aren't in sessionMeta.
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
	state          string        // claude/opencode/codex live state; "" for shell/ssm
	remoteURL      string        // claude Remote Control URL, "" otherwise
	context        *contextUsage // claude context fill, nil otherwise
	resumable      bool          // false = stopped agent whose working dir is gone
	backgroundBusy bool          // claude: idle turn but a run_in_background task lingers
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
	buildLaunch(m sessionMeta, opts launchOpts) (launchPlan, error)
	// wireLive computes the live-dependent Session fields for the sessions list.
	wireLive(m sessionMeta, alive bool) liveInfo
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
	transcript(m sessionMeta) (transcriptData, bool)
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
	// pendingQuestions so the Console can render it interactively.
	pending []transcript.Question
}

// noGenericTranscript is the transcript() default for agents that either have no
// readable transcript (shell/ssm) or use their own path (claude). Embedding it keeps
// the interface satisfied without a per-agent stub.
type noGenericTranscript struct{}

func (noGenericTranscript) transcript(sessionMeta) (transcriptData, bool) {
	return transcriptData{}, false
}

// agents is the kind → Agent registry. agentOf falls back to claude for an unknown
// or empty kind, matching the historical default (a session with no recognized kind
// launches claude).
var agents = map[string]Agent{
	kindClaude:   claudeAgent{},
	kindOpencode: opencodeAgent{},
	kindCodex:    codexAgent{},
	kindShell:    shellAgent{},
	kindSSM:      ssmAgent{},
}

func agentOf(kind string) Agent {
	if a, ok := agents[kind]; ok {
		return a
	}
	return agents[kindClaude]
}

// normalizeKind maps a create request's kind onto a registered one, defaulting the
// unknown/empty/"claude" cases to claude (the historical create whitelist).
func normalizeKind(kind string) string {
	if _, ok := agents[kind]; ok {
		return kind
	}
	return kindClaude
}

// --- shared live-state helpers -------------------------------------------------

// liveStateFromStatus reads the status file written by the agent's hooks/plugin,
// defaulting a live session with no recorded event to idle (sitting at the prompt).
func liveStateFromStatus(sid string) string {
	state := "idle"
	if st, ok := readSessionStatus(sid); ok {
		state = st.State
	}
	return state
}

// driveState is the live state for the drive endpoints (status/output/messages):
// "stopped" when not alive, else idle-or-recorded. heal self-corrects a stale
// non-idle cache when the claude pane is back at its ready prompt (killed+resumed,
// rejected permission, abandoned question) — /output opts out (heal=false) to match
// its historical behavior.
func driveState(m sessionMeta, alive, heal bool) string {
	if !alive {
		return "stopped"
	}
	// opencode: derive state from its own store (the status plugin is unreliable) so the
	// chat chip doesn't stick on 進行中 after a turn the plugin never reported idle for.
	if m.Kind == kindOpencode {
		if st := opencodeLiveState(m); st != "" {
			return st
		}
	}
	sid := sessionUUID(m.Dir, m.Name)
	state := liveStateFromStatus(sid)
	if heal && state != "idle" && sessionAtIdlePrompt(m.Name) {
		state = "idle"
		removeSessionStatus(sid)
	}
	return state
}

// statusOnlyLive is the wireLive body shared by opencode/codex: state from the
// status store (no idle-heal, no background-busy), and resumable unless the working
// dir is gone.
func statusOnlyLive(m sessionMeta, alive bool) liveInfo {
	li := liveInfo{resumable: true}
	if alive {
		li.state = liveStateFromStatus(sessionUUID(m.Dir, m.Name))
	} else if !dirExists(m.Dir) {
		li.resumable = false
	}
	return li
}

// --- sid store -----------------------------------------------------------------

// sidStore maps our deterministic slot sid to an agent's own session id, so a slot
// resumes its OWN conversation (fstore.go の fileStore に薄い読み口を被せたもの:
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
	opencodeSids = sidStore{fstore.TrimmedStrings(agentConfigDir, "opencode-sid")}
	// codex: written by the session-status hook from codex's own session_id (codex
	// has no --session-id flag to pin), read for `codex resume <id>`.
	codexSids = sidStore{fstore.TrimmedStrings(agentConfigDir, "codex-sid")}
)

// --- claude --------------------------------------------------------------------

type claudeAgent struct{ noGenericTranscript }

func (claudeAgent) kind() string { return kindClaude }

func (claudeAgent) caps() agentCaps {
	return agentCaps{canFork: true, canTranscript: true, usesLabel: true}
}

func (claudeAgent) buildLaunch(m sessionMeta, _ launchOpts) (launchPlan, error) {
	// A claude session must launch in its real working dir: if the dir is gone (its
	// repo was deleted) we refuse rather than resume the conversation in an unrelated
	// cwd. wireSession reports this as non-resumable.
	if !dirExists(m.Dir) {
		return launchPlan{}, dirGoneErr(m.Dir)
	}
	// Pre-trust the launch dir so claude doesn't stall on the folder-trust dialog
	// (not skippable via --dangerously-skip-permissions).
	ensureFolderTrusted(m.Dir)
	sid := sessionUUID(m.Dir, m.Name)
	// A jsonl can exist yet hold no real conversation — e.g. only a Remote Control
	// "bridge-session" line when RC connected but nothing was said. claude --resume
	// then dies with "No conversation found". Drop such a stub so buildSessionProgram
	// starts fresh (--session-id) instead of resuming.
	if !jsonlResumable(sid) {
		for _, p := range jsonlPaths(sid) {
			_ = os.Remove(p)
		}
	}
	// No env token is injected: the interactive TUI authenticates from claude's own
	// .credentials.json, written by `claude auth login` via the Connections flow
	// (claude_auth.go). CLAUDE_CODE_OAUTH_TOKEN is headless-only.
	return launchPlan{program: buildSessionProgram(sid, m.Model, m.Label, m.ForkFrom), cwd: m.Dir}, nil
}

func (claudeAgent) wireLive(m sessionMeta, alive bool) liveInfo {
	li := liveInfo{resumable: true}
	sid := sessionUUID(m.Dir, m.Name)
	li.remoteURL = remoteSessionURL(sid)
	li.context = latestSessionContext(sid)
	if alive {
		// Default a live claude with no recorded event yet to idle (it sits at the
		// prompt waiting for input). Hook events refine it.
		li.state = liveStateFromStatus(sid)
		// Self-heal a stale cache: a non-idle state that no longer matches the terminal
		// (killed+resumed, rejected permission, abandoned question) — if the pane is
		// back at the ready prompt, it's idle.
		if li.state != "idle" && sessionAtIdlePrompt(m.Name) {
			li.state = "idle"
			removeSessionStatus(sid)
		}
		// Idle by hook, but a run_in_background task may still be running under the
		// pane — surface that so 入力待ち isn't mistaken for "done".
		if li.state == "idle" {
			li.backgroundBusy = sessionBackgroundBusy(m.Name)
		}
	} else if !dirExists(m.Dir) {
		// A stopped claude whose working dir was removed (its repo deleted) can't be
		// resumed there; the Console marks it non-resumable (archive only).
		li.resumable = false
	}
	return li
}

func (claudeAgent) clearResume(string) {}

// --- opencode ------------------------------------------------------------------

type opencodeAgent struct{}

func (opencodeAgent) kind() string { return kindOpencode }

// canTranscript lights up the Console chat mirror for opencode; its turns come from the
// SQLite store via transcript() (readOpencodeTranscript), windowed by the generic
// /messages handler. No fork/label/inline-questions (those are claude-specific).
func (opencodeAgent) caps() agentCaps { return agentCaps{canTranscript: true} }

func (opencodeAgent) transcript(m sessionMeta) (transcriptData, bool) {
	return readOpencodeTranscript(m)
}

func (opencodeAgent) buildLaunch(m sessionMeta, _ launchOpts) (launchPlan, error) {
	// opencode resumes (or starts) in its real project dir; refuse if it's gone.
	if !dirExists(m.Dir) {
		return launchPlan{}, dirGoneErr(m.Dir)
	}
	// AF_SESSION_SID lets the bundled opencode plugin report this session's
	// working/idle state back keyed by OUR deterministic sid (same store claude
	// uses), so wireSession can surface it. Provider API keys are injected as env
	// (ANTHROPIC_API_KEY, …) so opencode authenticates without a plaintext file. The
	// env is prefixed onto the command itself (not tmux -e, which sets only the
	// session environment and does NOT reach the pane's process).
	ocSid := sessionUUID(m.Dir, m.Name)
	envs := append([]string{"AF_SESSION_SID=" + ocSid}, opencodeEnv()...)
	// Resume the slot's current opencode conversation, resolved from the store itself
	// (plugin-independent — see opencodeActiveSession), UNLESS its last turn was
	// interrupted (incomplete). opencode continues an incomplete turn on resume, re-running
	// the pending work (e.g. an Explore subagent the user stopped); starting fresh avoids
	// that. The interrupted conversation stays in the store, just not auto-resumed.
	resume := ""
	if db, ok := opencodeOpenRO(); ok {
		resume = opencodeActiveSession(db, m)
		db.Close()
	}
	if resume == "" {
		resume = opencodeSids.read(ocSid) // fallback when the store can't be read
	}
	if resume != "" && !opencodeSessionResumable(resume) {
		resume = ""
	}
	return launchPlan{program: buildOpencodeProgram(m.Model, envs, resume), cwd: m.Dir}, nil
}

func (opencodeAgent) wireLive(m sessionMeta, alive bool) liveInfo {
	// State is derived from opencode's own store (opencodeLiveState) — robust against the
	// status plugin not firing — falling back to the plugin status file when the db can't
	// be read. resumable unless the working dir is gone.
	li := liveInfo{resumable: true}
	if alive {
		if st := opencodeLiveState(m); st != "" {
			li.state = st
		} else {
			li.state = liveStateFromStatus(sessionUUID(m.Dir, m.Name))
		}
	} else if !dirExists(m.Dir) {
		li.resumable = false
	}
	return li
}

func (opencodeAgent) clearResume(sid string) { opencodeSids.remove(sid) }

// --- codex ---------------------------------------------------------------------

type codexAgent struct{}

func (codexAgent) kind() string { return kindCodex }

// canTranscript lights up the Console chat mirror for codex; its turns come from the
// rollout JSONL via transcript() (readCodexTranscript), windowed by the generic
// /messages handler. No fork/label (codex has no --session-id pin nor --name).
func (codexAgent) caps() agentCaps { return agentCaps{canTranscript: true} }

func (codexAgent) transcript(m sessionMeta) (transcriptData, bool) {
	return readCodexTranscript(m)
}

func (codexAgent) buildLaunch(m sessionMeta, _ launchOpts) (launchPlan, error) {
	// codex resumes (or starts) in its real project dir; refuse if it's gone.
	if !dirExists(m.Dir) {
		return launchPlan{}, dirGoneErr(m.Dir)
	}
	// Pre-accept codex's per-dir trust gate so a freshly cloned repo doesn't stall at
	// the "Do you trust this directory?" prompt (the bypass flags don't cover it).
	ensureCodexFolderTrusted(m.Dir)
	// Auth is codex's own ~/.codex/auth.json (codex login, written via the Connections
	// flow), so no token is injected. State + per-slot resume are wired purely through
	// codex hooks injected on the command line (-c), keyed by our deterministic slot
	// sid — see buildCodexProgram.
	cxSid := sessionUUID(m.Dir, m.Name)
	return launchPlan{program: buildCodexProgram(m.Model, cxSid, codexSids.read(cxSid)), cwd: m.Dir}, nil
}

func (codexAgent) wireLive(m sessionMeta, alive bool) liveInfo {
	// State comes from codex's -c-injected status hooks keyed by our sid.
	return statusOnlyLive(m, alive)
}

func (codexAgent) clearResume(sid string) { codexSids.remove(sid) }

// --- shell ---------------------------------------------------------------------

type shellAgent struct{ noGenericTranscript }

func (shellAgent) kind() string    { return kindShell }
func (shellAgent) caps() agentCaps { return agentCaps{} }

func (shellAgent) buildLaunch(m sessionMeta, _ launchOpts) (launchPlan, error) {
	// A shell falls back to home if its recorded dir is gone.
	cwd := m.Dir
	if !dirExists(cwd) {
		cwd = homeDir()
	}
	return launchPlan{program: "bash -l", cwd: cwd}, nil
}

func (shellAgent) wireLive(m sessionMeta, alive bool) liveInfo {
	return liveInfo{resumable: true}
}

func (shellAgent) clearResume(string) {}

// --- ssm -----------------------------------------------------------------------

type ssmAgent struct{ noGenericTranscript }

func (ssmAgent) kind() string    { return kindSSM }
func (ssmAgent) caps() agentCaps { return agentCaps{} }

func (ssmAgent) buildLaunch(m sessionMeta, opts launchOpts) (launchPlan, error) {
	// An SSM session logs into the operator's OWN AWS via `aws sso login` (the
	// device-code URL is surfaced in this terminal — click it to authenticate in
	// another tab) then opens Session Manager on the target instance. No AWS
	// credentials pass through Agent Fleet: the aws CLI authenticates directly and
	// caches the short-lived token in the home volume. Launch dir is home (the work
	// happens on the remote instance).
	if m.SSM == nil || m.SSM.Target == "" {
		return launchPlan{}, errSSMNoTarget
	}
	p, err := buildSSMProgram(m.Name, *m.SSM, opts.ssmForce)
	if err != nil {
		return launchPlan{}, err
	}
	return launchPlan{program: p, cwd: homeDir()}, nil
}

func (ssmAgent) wireLive(m sessionMeta, alive bool) liveInfo {
	return liveInfo{resumable: true}
}

func (ssmAgent) clearResume(string) {}
