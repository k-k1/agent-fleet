// Package session holds the session model (wire structs, persisted meta, kind constants) and
// its helpers (tmux naming convention, UUID, meta persistence). Extracted from package main
// (docs/log/23 remaining item 1 Wave A): the JSON tags and the on-disk layout must stay
// byte-identical to what main wrote.
package session

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Canonical kind list. These are the persisted Meta.Kind / wire Session.Kind
// values; keep them in sync with the agent registry (package main) and the
// Console's Session type.
const (
	KindClaude   = "claude"
	KindOpencode = "opencode"
	KindCodex    = "codex"
	KindCursor   = "cursor"
	KindKiro     = "kiro"
	KindAgy      = "agy"
	KindCopilot  = "copilot"
	KindShell    = "shell"
	KindSSM      = "ssm"
)

// Driver (docs/log/27 §2, §9.2, ADR 0015): a session's control route. tui is the traditional
// TUI inside tmux (AF writes with send-keys and reads by scraping/hooks). managed is a shared
// runtime plus structured RPC — AF is the only writer and there is no tmux pane. kind is NOT
// split along this line: transcript / settings / auth / models are shared with tui, so driver
// is a field on Meta instead (ADR 0015 decision 9.2).
const (
	DriverTUI     = "tui"
	DriverManaged = "managed"
)

// A session's origin (docs/log/46 §2-c, ADR 0029 §6): whose consumption this session is. It
// is a different axis from where a turn was injected from (transcript.Turn.Source), and it
// exists so usage accounting can separate "a session I opened myself" from "a session an
// operator raised on its own" — the latter grows unattended once autonomous runs and
// scheduled execution are in play.
const (
	OriginUser     = "user"     // a person started it from the Console's launch flow (default)
	OriginOperator = "operator" // create_session by the af_write assistant (and its conversation)
	OriginSchedule = "schedule" // raised by scheduled execution (docs/log/38)
	OriginHandoff  = "handoff"  // grown out of a handoff (formerly fork)
	OriginSession  = "session"  // create_session by another SESSION (ADR 0073) — see OriginSession field
	// OriginUnknown is a session that predates this feature. It keeps "neither zero nor user".
	OriginUnknown = "unknown"
)

// Who last set a session's display title (Meta.TitleSetBy). Only these two spellings and the
// empty string exist; they are constants rather than literals because three packages compare
// against them (the rename refusal, the MCP tool, the wire) and a typo on one side would read
// as "nobody set it" — the value that refuses nothing.
const (
	TitleSetByUser   = "user"   // the user renamed it in the Console; the parent may not rewrite it
	TitleSetByParent = "parent" // the spawning parent renamed it with rename_child_session
)

// SpawnChildLimitDefault is how many children one session may have at a time when the user has
// chosen nothing (ADR 0073 decision 6). Deliberately small: this is the first session-side
// capability that consumes the shared host's memory with nobody watching.
//
// It is NOT a measured resource limit — it is the number a refusal can name, and the tool
// description states it so a caller learns the ceiling before planning around one it does not
// have. Making it configurable did not change that: SpawnChildLimit is read afresh at every
// point that says the number out loud, so no caller is ever told a figure that is not in force.
const SpawnChildLimitDefault = 3

// SpawnChildLimitMax is the largest value the setting may take (ADR 0073 decision 6, amendment
// 2026-09-10). A live claude session measures 340-435 MB and this host's cgroup is 10 GiB
// (docs/log/88 §88.9.2), so six children plus their parent is under a third of the host —
// leaving room for the sessions the user opened themselves.
//
// The ceiling has to be well under what the host can hold, because this budget is PER PARENT and
// nothing bounds their number: two parents at six is already thirteen agents. Refusing to have an
// upper bound at all would give back the one property the limit exists for — a ceiling a refusal
// can name before the host runs out of memory instead of after.
const SpawnChildLimitMax = 6

// SpawnChildLimitPref answers the user's configured child limit, RAW: whatever number is stored,
// or 0 for missing or malformed. Wired by internal/uiprefs, which cannot be imported from here
// (it depends on this package). Nil means "nothing is wired", i.e. the default.
//
// The range and the fallback live in NormalizeSpawnChildLimit rather than in the setter, so the
// Agent (which enforces the budget) and the MCP layer (which advertises it and writes it into
// refusals) cannot drift into two answers.
var SpawnChildLimitPref func() int

// SpawnChildLimit is the child limit in force. Call it at the moment the number is needed —
// enforcing, advertising, refusing — never once into a variable: the user can change the setting
// between two turns of the same session, and a stale figure in a refusal is exactly the invisible
// limit decision 6 refuses to have.
func SpawnChildLimit() int {
	if SpawnChildLimitPref == nil {
		return SpawnChildLimitDefault
	}
	return NormalizeSpawnChildLimit(SpawnChildLimitPref())
}

// NormalizeSpawnChildLimit narrows a stored value into the permitted range. Anything outside
// 1..SpawnChildLimitMax reads as the default rather than as the nearest bound: the Console offers
// a fixed set of choices, so an out-of-range value can only be a hand-edited or stale prefs file,
// and "what the workspace does with no setting" is a better answer to that than a number nobody
// picked. Same rule as the other uiprefs accessors — invalid is not a choice.
func NormalizeSpawnChildLimit(n int) int {
	if n < 1 || n > SpawnChildLimitMax {
		return SpawnChildLimitDefault
	}
	return n
}

// ValidOrigin narrows an origin arriving from outside into the recordable vocabulary. The
// create wire field is reachable from any client, so an unknown value degrades to user (a
// person's action, which passes with no label) and no arbitrary string enters the accounting
// dimension.
func ValidOrigin(s string) string {
	switch s {
	case OriginUser, OriginOperator, OriginSchedule, OriginHandoff, OriginSession, OriginUnknown:
		return s
	}
	return OriginUser
}

// OriginOf is the origin used for accounting. A pre-existing meta without the field reads as
// unknown; guessing user would overstate "what people opened themselves".
func OriginOf(m Meta) string {
	if m.Origin == "" {
		return OriginUnknown
	}
	return m.Origin
}

// InUnattendedChain reports whether m sits inside a chain of sessions that grew with nobody
// opening one — the question the recursion limit answers (ADR 0073 decision 5).
//
// ⚠️ Never open-code this as `OriginSession != ""`. That WAS the whole predicate while
// origin_session had a single producer, and it still reads as obviously right, which is
// exactly why the check lives behind a name: OriginSession now also carries "which session
// proposed the handoff a person launched from", and that session is origin=user.
//
// Both terms are load-bearing, in opposite directions:
//   - a lineage alone is not enough — a handoff proposal's launch has one and IS a human
//     re-entry, the one decision 5 deliberately leaves open;
//   - origin alone is not enough either — forking a child stamps origin=handoff while keeping
//     the lineage, so a rule reading origin would let the fork spawn.
//
// user is the only origin that reads as attended. handoff, session, schedule, operator and a
// meta too old to say (unknown) all answer true when a lineage is present: when in doubt about
// a chain nobody is watching, refuse.
func InUnattendedChain(m Meta) bool {
	return m.OriginSession != "" && OriginOf(m) != OriginUser
}

// tmux session naming: friendly name "slot01" <-> tmux "claude_slot01".
const TmuxPrefix = "claude_"

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

// ValidName reports whether s is a well-formed session name (slug).
func ValidName(s string) bool { return nameRe.MatchString(s) }

// Session is the wire representation of a Claude session (one tmux session).
type Session struct {
	Name string `json:"name"`
	Tmux string `json:"tmux"`
	Dir  string `json:"dir"`
	Kind string `json:"kind"` // "claude" | "opencode" | "codex" | "shell"
	// Driver mirrors Meta.Driver on the wire ("" = tui). A managed session has no tmux
	// pane, so the Console reads this, skips the terminal view and renders the mirror
	// (chat) as the primary UI (docs/log/27 §10).
	Driver string `json:"driver,omitempty"`
	// Subdir mirrors Meta.Subdir: the folder beneath Dir the agent actually runs in
	// ("" = Dir itself). Dir stays the working copy, so the Console keeps grouping
	// sessions by copy and only shows this as extra "where inside it" detail.
	Subdir string `json:"subdir,omitempty"`
	// Origin / OriginSession mirror Meta's provenance onto the wire (ADR 0073). Origin is
	// always present (OriginOf, so a session older than the feature reads "unknown");
	// OriginSession is the session this one came from — the parent that spawned it
	// (origin=session), or the one whose handoff proposal a person launched (origin=user).
	//
	// The consumer the earlier note here waited for is list_child_sessions (docs/log/89): a
	// parent asking "which of these are mine" reads exactly this pair off GET /sessions, which
	// also gets it the live state and the archived/TTL filtering the list already applies.
	Origin        string `json:"origin,omitempty"`
	OriginSession string `json:"originSession,omitempty"`
	Repo          string `json:"repo"` // working dir basename (display)
	WorkingCopyID string `json:"workingCopyId,omitempty"`
	Title         string `json:"title"` // user-supplied display title (optional, any kind)
	// TitleSetBy mirrors Meta.TitleSetBy ("user" | "parent" | ""): who last set Title. It
	// rides the wire so a reader outside this process can tell a name the user chose from one
	// a parent wrote — the same reason Origin / OriginSession are here. Display-only for now;
	// the refusal itself is enforced at the Agent's rename endpoint, never on the wire.
	TitleSetBy string `json:"titleSetBy,omitempty"`
	Display    string `json:"display"`   // human-readable name (title → claude label → repo@time); never the slug alone
	Color      string `json:"color"`     // terminal background hue (hex); SSM carries its host color
	Label      string `json:"label"`     // claude --name display name (claude only)
	Started    string `json:"started"`   // "01/02 15:04" local time, for the list
	CreatedAt  string `json:"createdAt"` // RFC3339
	RemoteUrl  string `json:"remoteUrl"` // claude.ai Remote Control URL, when RC is bridged
	State      string `json:"state"`     // claude live state: working | idle | question | ""
	Alive      bool   `json:"alive"`     // true = live tmux session; false = stopped
	Resumable  bool   `json:"resumable"` // false = stopped claude whose working dir is gone
	// BackgroundBusy: state is idle (turn done) but a run_in_background task is still
	// running under the pane. Lets the Console mark a session that is waiting for input
	// as "still working in bg".
	BackgroundBusy bool `json:"backgroundBusy"`
	// BackgroundBusyReason: WHAT is running behind the idle prompt — "process" (a
	// run_in_background worker), "subagent" (a background Task/Workflow agent, which
	// spawns no process), "shell" (a Monitor / waiting background shell). Display only:
	// the badge lights on BackgroundBusy, this only chooses its wording, so an unknown
	// (or dropped) value falls back to the generic "running in background".
	BackgroundBusyReason string `json:"backgroundBusyReason,omitempty"`
	// RateLimitResumeAt is set ONLY when State == agents.StateLimited: the time (RFC3339) of
	// the scheduled automatic resume. Empty = stopped at the limit with no resume armed
	// (auto-resume off, nothing to derive the reset time from, or a per-model limit —
	// docs/log/47 §4-5). Display only, so the chip can say when it will move again; the
	// waiting itself belongs to the CP's scheduled execution.
	RateLimitResumeAt string `json:"rateLimitResumeAt,omitempty"`
	// AuthOkAt is when the agent's login in force RIGHT NOW was written (RFC3339), claude
	// only, empty when there is nothing to judge on (claude.AuthOKAt). Display only, and
	// read against a TURN'S time rather than the clock: an error block whose turn is older
	// than this has already been re-authenticated, so the mirror stops offering a fix for
	// something the user has already fixed (docs/log/47 §4-11).
	AuthOkAt string `json:"authOkAt,omitempty"`
	// Context: current context-window fill (newest assistant turn's prompt tokens),
	// claude only, nil when none recorded yet. Drives the Console's ContextBar in
	// both the terminal and chat heads without a separate transcript poll.
	Context *ContextUsage `json:"context,omitempty"`
	// Branch is the session's start branch (Meta.Branch). CurrentBranch is the
	// working copy's branch right now; it is set ONLY when it differs from Branch, at
	// which point BranchDrift is true — the working tree was switched under the session
	// (a checkout that bypassed the guard). The Console badges the row so the mishap is
	// visible even though it can't be prevented at the git layer.
	Branch        string `json:"branch,omitempty"`
	CurrentBranch string `json:"currentBranch,omitempty"`
	BranchDrift   bool   `json:"branchDrift,omitempty"`
	// Worktree marks a session running in a linked git worktree — the Console offers
	// branch rename (deferred naming) only for these, since renaming a standalone
	// clone's branch is a different, rarer intent.
	Worktree bool `json:"worktree,omitempty"`
	// ExitReason explains why a STOPPED session's agent process terminated, when the
	// pane exit recorder caught an abnormal end: "oom" (memory-killed), "killed"
	// (SIGKILL, non-OOM), or "crashed" (fault / non-zero exit). Empty for live sessions,
	// clean quits, and deliberate stops — those show the plain "stopped" chip. ExitCode is
	// the raw pane wait status (128+signal on a kill; 137 = OOM SIGKILL) and ExitSignal
	// the derived signal number, both surfaced in the row tooltip.
	ExitReason string `json:"exitReason,omitempty"`
	ExitCode   int    `json:"exitCode,omitempty"`
	ExitSignal int    `json:"exitSignal,omitempty"`
	// Carried is the kind of interaction that was on screen when the session was folded
	// away ("question" | "plan" | "permission"). Set only on stopped rows
	// (docs/log/75 §75.6.5).
	//
	// Why the list needs it: a stopped session's state is the single word "stopped", and
	// the fact that a question is waiting for an answer showed up nowhere at all. Once
	// waiting on a person can be folded away (docs/log/75 P2), a folded question that is
	// invisible in the list cannot be told apart from one silently lost. The card is there
	// if you open the mirror, but nobody opens it without a reason to.
	Carried string `json:"carried,omitempty"`
	// HandoffPending: the LAST successor's first prompt this session proposed
	// (propose_session_handoff) has not been launched. It clears the moment that proposal is
	// launched or discarded — or a newer one is launched in its place, since proposals are
	// kept after launch and an older one a later proposal replaced is not pending work.
	//
	// Why the list needs it, for the same reason as Carried above: the proposal exists only
	// as a card in the mirror and raises no notification, while the session that made it goes
	// idle — the chip of a session with nothing left to do. The next step of the work is then
	// invisible until someone happens to open that conversation. Sent on stopped rows too: a
	// session folded away with an unlaunched handoff is exactly the one nobody will reopen.
	HandoffPending bool `json:"handoffPending,omitempty"`
	// Locked mirrors Meta.Locked: the user pinned this session against deletion, so
	// every removal path (stop=forget meta / delete / TTL prune / a working-copy
	// delete that would take it down with it) refuses until it is unlocked. The
	// Console badges the row and disables its delete item off this flag.
	Locked   bool `json:"locked,omitempty"`
	Archived bool `json:"archived,omitempty"`
	// KeepAwakeUntil mirrors Meta.KeepAwakeUntil: while it is in the future the
	// idle-stop reaper leaves this session AND its workspace alone (docs/log/75). Carried
	// on stopped rows too — a pin that is set has to be visible before it expires, or it
	// cannot be released.
	KeepAwakeUntil string `json:"keepAwakeUntil,omitempty"`
	// LastTurnEndAt is when this session's newest turn actually ENDED (RFC3339), and empty
	// when nothing here can say so. It answers "is this one finished?" for a reader with no
	// eyes on the mirror — a parent polling its children (ADR 0073 decision 9), where the
	// state word alone cannot distinguish a child that has finished from one that has not
	// started.
	//
	// ⚠️ It is present only while the newest thing that happened IS a turn ending: a session
	// that is mid-turn, or that was restarted since (SessionStart resets the marker), reads
	// empty. That is the point rather than a gap — the underlying bit exists precisely to keep
	// "the turn ended" apart from "an idle we cannot explain" (status.SessionStatus.TurnEnd,
	// docs/log/51), and a fabricated time would be read as evidence of completion.
	LastTurnEndAt string `json:"lastTurnEndAt,omitempty"`
	// StopAfterTurnAt mirrors Meta.StopAfterTurnAt: the session is armed to stop itself at
	// the end of the running turn (docs/log/85). The row has to say so, because the arm is
	// usually set from inside the conversation (the MCP tool) where the user only sees prose
	// claiming it was set, and because a session that is about to fold itself away must be
	// cancellable before it does.
	StopAfterTurnAt string `json:"stopAfterTurnAt,omitempty"`
}

// ContextUsage is a claude session's current context fill — the newest assistant
// turn's prompt token breakdown. It is serialized into Session.Context so the
// Console can render the /context-like gauge in BOTH the terminal and chat heads
// straight off the sessions list, with no separate transcript poll. The field
// names match the Console's ContextBar props (read / create / fresh / model).
type ContextUsage struct {
	Read   int    `json:"read"`   // cache_read_input_tokens (reused cache)
	Create int    `json:"create"` // cache_creation_input_tokens (newly cached)
	Fresh  int    `json:"fresh"`  // input_tokens (uncached)
	Model  string `json:"model"`
}

func TmuxName(name string) string { return TmuxPrefix + name }

// ExactTarget returns a tmux target that matches NAME exactly. Without the leading
// '=', tmux's -t resolution prefix-matches, so a target like "claude_agent-fleet"
// would match an unrelated "claude_agent-fleet-sh" — wrongly reporting "already
// running" (blocking session creation) or killing the sibling on
// stop/archive/recreate.
func ExactTarget(tn string) string { return "=" + tn }

// Meta records how to (re)launch a session. tmux destroys a session when
// its program exits (e.g. the user quits claude), losing the kind/dir/model we
// need to relaunch. We persist it in the home volume so the session stays listed
// and clicking it re-runs claude --resume in the SAME session id (derived from
// dir+name). Home survives Stop→Start, so a stopped session remains listed and
// resumable across a Workspace restart (claude --resume reads the jsonl, also
// persisted). The dir is denylisted in the file browser. "Recreate" wipes home,
// intentionally clearing sessions too.
type Meta struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
	// Subdir narrows the agent's CWD to a folder BENEATH Dir (slash-relative, e.g.
	// "console/src"), chosen at launch. Dir stays the working copy root so everything
	// that reasons about the copy — worktree pruning, the checkout guard, cleanup
	// grouping, the Console's per-repo grouping — keeps working unchanged; only the
	// launched process starts deeper (see CWD). "" = start at Dir, the default.
	Subdir string `json:"subdir,omitempty"`
	Model  string `json:"model"`
	// Effort / Mode are the desired managed-thread settings. They live beside Model
	// so a successful dynamic change survives Agent/workspace restarts and is inherited
	// by fork/recreate. TUI sessions leave both empty.
	Effort string `json:"effort,omitempty"`
	Mode   string `json:"mode,omitempty"`
	// SkipPermissions is this session's answer to "skip the permission prompts?"
	// (docs/log/76): true = launch with the fleet's default bypass (claude
	// --dangerously-skip-permissions and each kind's equivalent flag), false = ask for
	// approval on every tool run. Being THREE-valued is the point: nil means unspecified,
	// i.e. follow the per-kind default in ui-prefs. Without separating false from nil there
	// is no way to express "off in settings, back on for this one session".
	// It only takes effect at launch (a TUI needs a restart). A plan launch drops the
	// bypass for every kind, so mode=plan still prompts even when this is true.
	SkipPermissions *bool  `json:"skipPermissions,omitempty"`
	Kind            string `json:"kind"`
	// Driver selects the control route (docs/log/27): "" | "tui" = the traditional TUI
	// inside tmux, "managed" = a shared runtime plus structured RPC (no pane). The default
	// tui persists as "", keeping existing metas byte-identical on disk.
	Driver string `json:"driver,omitempty"`
	Title  string `json:"title"` // user-supplied display title (optional); "" = auto
	// SuggestedTitle is a headless-LLM-generated candidate the Console offers via a
	// dismissible banner once the session has had a couple of exchanges and has no
	// user title yet. "" = none pending (not generated yet, already accepted into
	// Title, or dismissed).
	SuggestedTitle string `json:"suggestedTitle,omitempty"`
	// SuggestedTitleDismissed latches true once the user accepts OR dismisses a
	// suggestion, so a session is offered one at most once (v1: no re-suggestion loop).
	SuggestedTitleDismissed bool `json:"suggestedTitleDismissed,omitempty"`
	// TitleSetBy records WHO last set Title: TitleSetByUser, TitleSetByParent, or "" for
	// the title a create carried (ADR 0073 decision 4, amended 2026-09-11). It exists for
	// one refusal — a parent may rename its own child, but not over a name the user chose
	// in the Console — and the asymmetry is the point: the user's rename always wins, so
	// the only value that refuses anything is "user".
	//
	// Clearing the title resets this to "", for the same reason it re-opens auto-suggestion:
	// the session is back in the state a fresh one is in, and nothing is being overwritten.
	TitleSetBy string `json:"titleSetBy,omitempty"`
	Color      string `json:"color"` // terminal background hue (hex); set at create (SSM host color)
	Label      string `json:"label"` // claude --name (display); derived from Title at create/recreate
	Repo       string `json:"repo"`  // working dir basename
	// Branch is the git branch the working copy (Dir) was on when this session was
	// created/recreated. Compared against Dir's current branch on each list to flag
	// drift — a `git checkout` that slipped past the checkout guard (agent/manual
	// shell inside the session). "" when Dir isn't a git working tree, or for
	// pre-existing sessions minted before this field. Never rewritten after create,
	// so the drift comparison stays meaningful.
	Branch    string `json:"branch,omitempty"`
	CreatedAt string `json:"createdAt"` // RFC3339, set at create
	StoppedAt string `json:"stoppedAt"` // RFC3339, set lazily when first seen exited; "" while live
	Archived  bool   `json:"archived"`  // true = hidden from the active list, restorable (jsonl kept)
	// Locked pins the session against deletion: /stop (forgetting the meta), DELETE
	// /sessions/{name}, the stopped-TTL auto prune and being taken down together with a
	// deleted working copy are all refused while it is locked (archiving is reversible, so
	// it stays allowed). Release with POST /sessions/{name}/lock {"locked":false}. The
	// protection lives in the Agent's REST layer, so a deletion coming from the Console, an
	// operator (MCP) or the bridge is stopped the same way.
	Locked bool `json:"locked,omitempty"`
	// KeepAwakeUntil pins the session (and with it the workspace) against the idle-stop
	// reaper until this instant (RFC3339). Empty / past = not pinned.
	//
	// Why an instant rather than a boolean (docs/log/75 §75.5): a pin nobody remembers to
	// clear becomes the same thing as a forgotten terminal tab — something that quietly
	// keeps billing. A real reason not to stop lasts a few hours; anything else should
	// expire on its own, and extending is one more press.
	//
	// Why it is needed at all: for shell / ssm sessions af cannot tell whether a job is
	// running right now (by foreground command name an abandoned less looks like a build,
	// and ssm always holds aws). Rather than guessing, the decision was to have the user
	// declare it.
	KeepAwakeUntil string `json:"keepAwakeUntil,omitempty"`
	// StopAfterTurnAt arms a one-shot self-stop (docs/log/85): the instant (RFC3339) the arm
	// was set. At the first end of turn observed AFTER it, the session is halted — the
	// resumable stop, so nothing is lost and the user can pick the session back up.
	//
	// Why an instant rather than a boolean, the same reason as KeepAwakeUntil and the
	// opposite of it (one says "do not stop", this one says "stop"): the value doubles as the
	// lower bound the completion evidence is cut by, so a leftover end-of-turn marker from
	// the PREVIOUS turn cannot fire it, and an arm nobody consumed expires on its own
	// (stopArmMaxAge) instead of folding a session away hours later, in the middle of
	// unrelated work.
	StopAfterTurnAt string `json:"stopAfterTurnAt,omitempty"`
	// ForkFrom is the SOURCE conversation id this session was forked from, in the
	// kind's own id space: claude = the source slot's sid (jsonl), opencode = its
	// ses_… id, codex = its session uuid. It only affects the FIRST launch — each
	// kind's BuildLaunch turns it into the CLI's fork invocation (claude --resume
	// <id> --fork-session --session-id <ownsid> / opencode --session <id> --fork /
	// codex fork <id>), which copies the source history into this session's own
	// conversation. Once that exists, later launches resume normally and ForkFrom
	// is ignored — a restart never re-forks. Empty for non-forked sessions.
	ForkFrom string `json:"forkFrom,omitempty"`
	// ForkAt narrows ForkFrom to a POINT in the source conversation: this session
	// carries the source's history up to — but NOT including — the anchored turn
	// (docs/log/55 §55.3). The value is whatever the kind's ForkAtResolver produced from the
	// Console's anchor, already translated into that engine's inclusivity (opencode =
	// the exclusive messageID, codex = the inclusive lastTurnId of the PREVIOUS turn).
	// Empty = whole-conversation fork, the pre-existing behaviour. Like ForkFrom it only
	// affects the FIRST launch; afterwards the session resumes its own conversation.
	ForkAt string `json:"forkAt,omitempty"`
	// Origin / OriginConv are this session's origin (the Origin* constants, ADR 0029 §6):
	// the axis that separates consumption a person started from consumption an operator or
	// a schedule ran unattended. Unset = a session older than the feature, which OriginOf
	// reads as unknown rather than folding it into the default user. OriginConv is the
	// originating assistant conversation's slug when origin=operator. A recreate inherits
	// the original origin; a handoff sets handoff.
	//
	// OriginSession names the session this one CAME FROM. It has two producers, and Origin
	// is what tells them apart (ADR 0073 decision 1 and its 2026-09-10 amendment):
	//   - origin=session — a session started this one through create_session. Resolved
	//     server-side from the calling MCP server's own $AF_SESSION_NAME, never from the wire.
	//   - origin=user — a person launched this one from that session's handoff PROPOSAL.
	//     Origin stays user because a person opened it, so ADR 0029 §6's accounting axis does
	//     not move; the pair arrives on the wire and is verified against the proposing
	//     session's stored proposals before it is recorded (CreateOrigin).
	//
	// Three rules read it and they deliberately do not agree. Steering and the child budget
	// need origin==session AND a matching OriginSession (only your own children, and a
	// person's fork must not spend your budget); the recursion limit asks the wider question
	// InUnattendedChain — never `OriginSession != ""`, which would refuse the human re-entry
	// the second producer records. Only fork and recreate inherit it, and fork only from a
	// source that is itself in an unattended chain.
	Origin     string `json:"origin,omitempty"`
	OriginConv string `json:"originConv,omitempty"`
	// OriginSession names the session that raised this one (ADR 0073). See Origin above.
	OriginSession string `json:"originSession,omitempty"`
	// SSM holds the (non-secret) coordinates for a kind=ssm session: which instance,
	// run-as document, region, and the SSO profile to authenticate with. Persisted so
	// a relaunch regenerates ~/.aws/config and re-runs `aws sso login` (if the cached
	// token expired) before start-session. No AWS credentials are stored anywhere —
	// the aws CLI obtains them via SSO at launch and caches them in the home volume.
	SSM *SSMMeta `json:"ssm,omitempty"`
}

// SSMMeta is the persisted, non-secret description of an SSM login target.
type SSMMeta struct {
	Profile   string `json:"profile"`   // ~/.aws/config profile name (derived from alias)
	Target    string `json:"target"`    // EC2 instance id (i-...)
	Document  string `json:"document"`  // run-as SSM document ("" = default shell)
	Region    string `json:"region"`    // instance region ("" = profile default)
	StartURL  string `json:"startUrl"`  // SSO access-portal start URL ("" = use existing ~/.aws)
	SSORegion string `json:"ssoRegion"` // SSO region
	AccountID string `json:"accountId"` // SSO account id
	RoleName  string `json:"roleName"`  // SSO permission-set role name
}

// DriverKind normalizes Meta.Driver ("" → tui). Always branch through it: comparing raw
// Meta.Driver makes each call site handle the default (empty string) its own way.
func (m Meta) DriverKind() string {
	if m.Driver == "" {
		return DriverTUI
	}
	return m.Driver
}

// StoppedTTL is how long a stopped (exited) session stays listed/resumable before
// it is pruned. Configurable; default 7d (metas now persist across Stop→Start, so
// the window spans restarts). A session running at shutdown is marked stopped on
// the next list after restart, starting its TTL then.
func StoppedTTL() time.Duration {
	if v := os.Getenv("AF_SESSION_STOPPED_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 7 * 24 * time.Hour
}

// Display derives a human-readable session name, mirroring the Console's
// displayName (lib/sessionview.ts): the user title if set; else a claude session's
// --name label (minus the "[AF:<name>] " tag, label.go); else "{repo}@MMDD-HHMM". The random slug
// (Name) is never surfaced alone — it's an opaque id users don't recognize, so callers
// (e.g. the Fleet Operator) should report Display, not Name.
func Display(m Meta) string {
	if m.Title != "" {
		return m.Title
	}
	if m.Label != "" {
		return StripLabel(m.Label)
	}
	base := m.Repo
	if base == "" {
		base = m.Name
	}
	if m.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, m.CreatedAt); err == nil {
			return base + " @" + t.Local().Format("0102-1504")
		}
	}
	return base
}

// DirExists reports whether p is an existing directory.
func DirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// CWD is the directory the session's agent process actually starts in: Dir, or the
// Subdir beneath it when one was chosen at launch. A Subdir that no longer exists
// (deleted, or a branch switch that removed the folder) falls back to Dir rather than
// failing the launch — a session must stay startable, and the working copy root is
// always a defensible place to land.
func (m Meta) CWD() string {
	if m.Subdir == "" {
		return m.Dir
	}
	p := filepath.Join(m.Dir, filepath.FromSlash(m.Subdir))
	if !DirExists(p) {
		return m.Dir
	}
	return p
}

// CleanSubdir normalizes a launch-time subdir into the slash-relative form Meta
// stores, and reports whether it is acceptable at all. Absolute paths and any ".."
// escape are rejected outright: the field means "beneath the working copy", and a
// caller that wants another copy passes a different dir.
func CleanSubdir(s string) (string, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\\", "/"))
	if s == "" {
		return "", true
	}
	// Rejected BEFORE the slashes are trimmed: "/x" is almost always someone pasting an
	// absolute path, and quietly reading it as repo-relative would land them elsewhere.
	if strings.HasPrefix(s, "/") || filepath.IsAbs(s) || strings.HasPrefix(s, "~") {
		return "", false
	}
	if s = strings.Trim(s, "/"); s == "" {
		return "", true
	}
	c := path.Clean(s)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	return c, true
}
