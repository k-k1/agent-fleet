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
	// OriginUnknown is a session that predates this feature. It keeps "neither zero nor user".
	OriginUnknown = "unknown"
)

// ValidOrigin narrows an origin arriving from outside into the recordable vocabulary. The
// create wire field is reachable from any client, so an unknown value degrades to user (a
// person's action, which passes with no label) and no arbitrary string enters the accounting
// dimension.
func ValidOrigin(s string) string {
	switch s {
	case OriginUser, OriginOperator, OriginSchedule, OriginHandoff, OriginUnknown:
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
	Subdir        string `json:"subdir,omitempty"`
	Repo          string `json:"repo"` // working dir basename (display)
	WorkingCopyID string `json:"workingCopyId,omitempty"`
	Title         string `json:"title"`     // user-supplied display title (optional, any kind)
	Display       string `json:"display"`   // human-readable name (title → claude label → repo@time); never the slug alone
	Color         string `json:"color"`     // terminal background hue (hex); SSM carries its host color
	Label         string `json:"label"`     // claude --name display name (claude only)
	Started       string `json:"started"`   // "01/02 15:04" local time, for the list
	CreatedAt     string `json:"createdAt"` // RFC3339
	RemoteUrl     string `json:"remoteUrl"` // claude.ai Remote Control URL, when RC is bridged
	State         string `json:"state"`     // claude live state: working | idle | question | ""
	Alive         bool   `json:"alive"`     // true = live tmux session; false = stopped
	Resumable     bool   `json:"resumable"` // false = stopped claude whose working dir is gone
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
	SuggestedTitleDismissed bool   `json:"suggestedTitleDismissed,omitempty"`
	Color                   string `json:"color"` // terminal background hue (hex); set at create (SSM host color)
	Label                   string `json:"label"` // claude --name (display); derived from Title at create/recreate
	Repo                    string `json:"repo"`  // working dir basename
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
	Origin     string `json:"origin,omitempty"`
	OriginConv string `json:"originConv,omitempty"`
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
