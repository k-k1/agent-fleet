// Package agents は coding-agent 抽象の型層（Agent インターフェイスと入出力型）。
// docs/23 残① Wave C: package main の agent.go からインターフェイス層のみを切り出した。
// 依存は internal/session・internal/transcript のみ。実装（claude/opencode/codex/
// shell/ssm）とレジストリは後続 Wave で移すまで main に残る。
package agents

import (
	"errors"
	"fmt"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// DirGoneErr is the "can't (re)launch — working dir removed" error shared by the
// agents that require their real project dir (claude/opencode/codex).
func DirGoneErr(dir string) error {
	return fmt.Errorf("作業フォルダが存在しないため再開できません: %s", dir)
}

// ErrSSMNoTarget is returned when an ssm session has no connection target recorded.
var ErrSSMNoTarget = errors.New("SSM セッションの接続先が指定されていません")

// --- 権限確認をスキップするか（docs/76） ---------------------------------------
//
// fleet の既定は「スキップする」＝ claude なら --dangerously-skip-permissions、他 kind も
// 同格のフラグ（cursor --force / copilot --allow-all / kiro --trust-all-tools /
// agy --dangerously-skip-permissions）。コンテナ自体がサンドボックスで、承認ダイアログで
// TUI が止まっても Console から答えられない時期があったための判断だった。
// docs/76 でこれを**利用者の選択**にする。既定は変えない（true）。
//
// 値は 3 層で解決する。上から順に、最初に決まったものが勝つ:
//
//	① session.Meta.SkipPermissions — 起動時にこのセッションだけ明示した値
//	② ui-prefs agentLaunchDefaults[kind].skipPermissions — 設定 > エージェントの kind 毎の既定
//	③ true — 従来どおり
//
// ②を HTTP でなくプロセス内で読むのは、Console 以外の起動経路（MCP の create_session・
// 定時実行・再起動・fork・recreate）にも同じ既定を効かせるため。ここを Console 側だけの
// 解決にすると「設定でオフにしたのに定時実行のセッションだけ bypass で走る」が起きる。

// SkipPermissionsPref is the ui-prefs lookup seam (層②). package main が起動時に
// 実体（ui_prefs.go の skipPermissionsPref）を差す。ok=false は「その kind に設定が無い」。
// 既定値がここに書いていないのは、prefs を読めない/持たないビルド（テスト）でも
// 従来どおりに倒すため — 解決の既定は SkipPermissions が持つ。
var SkipPermissionsPref = func(kind string) (v bool, ok bool) { return false, false }

// SkipPermissions resolves the 3 層 for m. 「plan 起動なら承認を出す」は各 kind の
// buildProgram/spawn 側の判断（plan フラグの付与と対で扱う必要がある）なので、ここでは
// 見ない — この関数が答えるのは利用者の選択だけ。
func SkipPermissions(m session.Meta) bool {
	if m.SkipPermissions != nil {
		return *m.SkipPermissions
	}
	if v, ok := SkipPermissionsPref(m.Kind); ok {
		return v
	}
	return true
}

// BypassPermissions folds in plan mode: plan 起動は kind を問わず bypass を外す
// （全ツールを自動承認しては plan で始める意味が無い）。各 kind の BuildLaunch /
// Driver.Resume はこれを呼んで「bypass フラグを付けるか」を 1 つの bool で決める。
func BypassPermissions(m session.Meta) bool {
	return m.Mode != "plan" && SkipPermissions(m)
}

// Coding-agent abstraction. A session's "kind" (claude/opencode/codex/shell/ssm)
// used to be a bare string branched on in ~50 places — a new agent meant touching
// every switch/if. This package makes kind a canonical const list (now
// session.Kind*, internal/session) and folds the per-kind behavior behind an
// Agent interface + registry, so the diverging logic (how to launch the tmux
// program, what live state to surface, which capabilities exist) lives in ONE
// implementation per agent. Adding an agent = one Agent impl + one registry
// entry. The wire contract (session.Session/session.Meta JSON, HTTP shapes) is
// unchanged: the field VALUES are computed exactly as before, only the dispatch moved.

// Caps flags the optional features a kind supports, replacing the scattered
// `kind == "claude"` guards at the HTTP endpoints.
type Caps struct {
	CanFork       bool // POST /fork — copy the conversation into a new session (claude)
	CanTranscript bool // GET /output & /messages — read the jsonl transcript (claude)
	UsesLabel     bool // set a claude --name display label at create/recreate (claude)
	// PermissionChoice: 利用者が「権限確認をスキップするか」を選べる kind（docs/76）。
	// 立てる条件は**承認待ちが Console から答えられること**——フラグを外すだけなら
	// どの kind でもできるが、ペインしか無い（あるいはペインすら無い managed の）承認
	// ダイアログで止まったセッションは、利用者から見れば黙って固まったのと同じ。
	// 実測で導線がある kind にだけ立てる（「未検証の caps を立てない」— 1854d の教訓）。
	PermissionChoice bool
	// CanForkAt narrows CanFork: POST /fork {"at": …} can branch at a PAST turn instead
	// of copying the whole conversation (docs/55). Implies the kind also implements
	// ForkAtResolver and fills transcript.Turn.AnchorID. Never true without CanFork.
	CanForkAt bool
}

// LaunchOpts carries the per-launch inputs that aren't in session.Meta.
type LaunchOpts struct {
	SSMForce bool // ssm: force re-login (logout+login) instead of reusing a cached token
}

// LaunchPlan is what an Agent hands back to startSessionTmux: the pane program and
// the directory to launch it in (which may differ from meta.Dir — shell/ssm fall
// back to home).
type LaunchPlan struct {
	Program string
	Cwd     string
	// Env is KEY=VALUE pairs injected into the pane process via `tmux new-session
	// -e` (verified to reach the pane process on the image's tmux 3.3a). Secrets
	// belong here, NOT prefixed onto Program — a command-string prefix lands in
	// /proc/*/cmdline and tmux's pane_start_command, readable by anything in the
	// container.
	Env []string
}

// LiveInfo is the slice of a wire Session whose values depend on the kind and live
// state. wireSession fills the static fields and asks the agent for this.
type LiveInfo struct {
	State          string                // claude/opencode/codex live state; "" for shell/ssm
	RemoteURL      string                // claude Remote Control URL, "" otherwise
	Context        *session.ContextUsage // claude context fill, nil otherwise
	Resumable      bool                  // false = stopped agent whose working dir is gone
	BackgroundBusy bool                  // claude: idle turn but a run_in_background task lingers
}

// Agent is the per-kind behavior seam. Implementations are stateless value types
// (the registry holds one of each); all session state is derived from the passed
// meta and the on-disk stores.
type Agent interface {
	Kind() string
	Caps() Caps
	// BuildLaunch returns the tmux pane program + launch dir for m, or an error when
	// the session can't start (e.g. its working dir is gone). The common tmux
	// plumbing + toolchain prefix is applied by startSessionTmux.
	BuildLaunch(m session.Meta, opts LaunchOpts) (LaunchPlan, error)
	// WireLive computes the live-dependent Session fields for the sessions list.
	WireLive(m session.Meta, alive bool) LiveInfo
	// ClearResume forgets any captured per-slot resume id so recreate starts a fresh
	// conversation. No-op for agents that pin their own session id (claude) or keep
	// no resume state (shell/ssm).
	ClearResume(sid string)
	// Transcript returns the session's full chronological chat turns (normalized to the
	// common transcript.Turn model) plus diagnostics and the reconstructed ToDo list, for agents
	// whose native store isn't claude's <sid>.jsonl (codex rollout, opencode SQLite).
	// ok=false means the agent has no generic transcript source — claude uses its own
	// jsonl path in handleSessionMessages instead. The generic /messages handler windows
	// the turns and surfaces the tasks.
	Transcript(m session.Meta) (TranscriptData, bool)
}

// GracefulStopper is an optional Agent capability: a chance for the CLI to
// exit on its own terms before the pane is hard-killed. agy needs it because
// v1.1.4 flushes its cwd→conversation map (the resume-UUID source) ONLY on a
// graceful exit (統合E2E実測 — docs/32); kill-session would lose the id for
// good. Returning true means the tmux session already ended and the caller
// must skip its own kill.
type GracefulStopper interface {
	GracefulStop(m session.Meta) bool
}

// ModelChoice is one launch-time model option for the Console's model picker:
// ID is what the launch command receives (`codex -m` / `opencode --model`),
// Label what the picker shows. Served by GET /agents/{kind}/models.
type ModelChoice struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Efforts       []string `json:"efforts,omitempty"`
	DefaultEffort string   `json:"defaultEffort,omitempty"`
}

// TranscriptData is what a non-claude agent's Transcript() yields: the full
// chronological turns, the source path (diagnostics), and the current ToDo list
// (reconstructed from the agent's plan/todo state; nil when none).
type TranscriptData struct {
	Turns []transcript.Turn
	Path  string
	Tasks []transcript.Task
	// Mode is the agent's current permission/collaboration mode, normalized to "plan"
	// (plan mode) or "normal", so the Console can show the plan indicator and drive the
	// plan-mode toggle. "" when unknown.
	Mode string
	// Pending is the question the agent is currently awaiting an answer to (codex
	// request_user_input / opencode question tool), or nil. Surfaced like claude's
	// pending questions so the Console can render it interactively.
	Pending []transcript.Question
	// Queued are prompts typed into the RUNNING turn but not yet injected as a user
	// message (opencode's session_input rows awaiting promotion) — surfaced as the
	// mirror's キュー済み badge, like claude's queue-operation reconstruction.
	Queued []string
	// Compacting reports the agent is compacting its conversation right now
	// (opencode session.time_compacting) — surfaced as the mirror's 圧縮中 badge.
	Compacting bool
}

// ContextReporter is an optional Agent capability: a session-level context-fill
// reading for agents whose transcript carries no per-turn token usage (agy —
// transcript_full.jsonl に token 数が一切無い、docs/32). Called ONLY from the
// /messages handler (the chat mirror's poll), NOT from the bulk /sessions/usage
// aggregation, so a fleet-wide usage query never triggers the underlying
// (expensive, PTY-scrape) refresh. nil = no reading yet.
type ContextReporter interface {
	ContextFill(m session.Meta) *transcript.Context
}

// Forker is the optional fork capability behind Caps().CanFork: ForkSource resolves
// the source session's provider-native conversation id (claude sid / opencode ses_… /
// codex session uuid) for the new session's ForkFrom, or an error when there is no
// forkable conversation yet. The fork itself happens on the new session's first
// launch (each kind's BuildLaunch turns ForkFrom into its CLI's fork invocation).
type Forker interface {
	ForkSource(m session.Meta) (string, error)
}

// ForkAtResolver is the optional "fork at a point" capability behind Caps().CanForkAt
// (docs/55). It is DELIBERATELY separate from Forker: adding a method there would break
// the kinds that only do whole-conversation forks.
//
// ResolveForkAt validates an anchor (transcript.Turn.AnchorID, echoed back by the
// Console) against this session's own conversation and returns the value the kind's fork
// path needs — which is NOT always the anchor itself, because the engines disagree on
// inclusivity: opencode's messageID is exclusive and passes straight through, codex's
// lastTurnId is inclusive so the PREVIOUS turn's id comes back. Every kind resolves to
// the same user-facing meaning: keep the history up to, but not including, the anchored
// turn. An unknown / unusable anchor is an error — never a silent fall back to a
// whole-conversation fork, which would look like it worked.
type ForkAtResolver interface {
	ResolveForkAt(m session.Meta, at ForkPoint) (string, error)
}

// ForkPoint is what the user pointed at, before any per-engine translation.
type ForkPoint struct {
	// Anchor is the transcript.Turn.AnchorID of the USER turn the user clicked.
	Anchor string
	// Include keeps that turn and the reply it got, branching from just AFTER them
	// ("この発言の続きから") instead of just before them ("この発言をやり直す").
	//
	// Both are the same operation one exchange apart, which is why they share a resolver:
	// each kind converts (anchor, include) into its own "keep up to here" value, and
	// everything downstream — session.Meta.ForkAt, the launch paths — stays unchanged.
	// A kind resolves Include on the LAST exchange to "" (= the whole conversation),
	// which is the correct answer there: keeping everything through the final turn IS
	// the whole conversation.
	Include bool
}

// ErrForkAtRoute is what a ForkAtResolver returns (wrapped, so the message can say which
// kind and why) when the session's LAUNCH ROUTE cannot carry a fork point at all — not
// when the anchor is bad. The two need separating because the user's next move differs:
// a route problem means "this session can never do it" (switch to managed, or don't offer
// the affordance), a bad anchor means "try again / reload the mirror".
//
// The rule is per-kind, not global: opencode and codex can only fork at a point through
// their runtime APIs, so their CLI-route sessions refuse; claude has no managed driver at
// all and does the cut on its own transcript, so a TUI session is the only route it has.
var ErrForkAtRoute = errors.New("this session's launch route cannot fork at a past message")

// NoGenericTranscript is the Transcript() default for agents that either have no
// readable transcript (shell/ssm) or use their own path (claude). Embedding it keeps
// the interface satisfied without a per-agent stub.
type NoGenericTranscript struct{}

func (NoGenericTranscript) Transcript(session.Meta) (TranscriptData, bool) {
	return TranscriptData{}, false
}
