package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// セッションのモデルとワイヤ変換。メタ永続化= session_meta.go / tmux= session_tmux.go /
// HTTPハンドラ= session_handlers.go / CLI起動コマンド= session_program.go（docs/23 P1-W4）

// tmux session naming: friendly name "slot01" <-> tmux "claude_slot01".
const tmuxPrefix = "claude_"

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

// Session is the wire representation of a Claude session (one tmux session).
type Session struct {
	Name      string `json:"name"`
	Tmux      string `json:"tmux"`
	Dir       string `json:"dir"`
	Kind      string `json:"kind"`      // "claude" | "opencode" | "codex" | "shell"
	Repo      string `json:"repo"`      // working dir basename (display)
	Title     string `json:"title"`     // user-supplied display title (optional, any kind)
	Display   string `json:"display"`   // human-readable name (title → claude label → repo@time); never the slug alone
	Color     string `json:"color"`     // terminal background hue (hex); SSM carries its host color
	Label     string `json:"label"`     // claude --name display name (claude only)
	Started   string `json:"started"`   // "01/02 15:04" local time, for the list
	CreatedAt string `json:"createdAt"` // RFC3339
	RemoteUrl string `json:"remoteUrl"` // claude.ai Remote Control URL, when RC is bridged
	State     string `json:"state"`     // claude live state: working | idle | question | ""
	Alive     bool   `json:"alive"`     // true = live tmux session; false = stopped
	Resumable bool   `json:"resumable"` // false = stopped claude whose working dir is gone
	// BackgroundBusy: state is idle (turn done) but a run_in_background task is still
	// running under the pane. Lets the Console mark 入力待ち as "still working in bg".
	BackgroundBusy bool `json:"backgroundBusy"`
	// Context: current context-window fill (newest assistant turn's prompt tokens),
	// claude only, nil when none recorded yet. Drives the Console's ContextBar in
	// both the terminal and chat heads without a separate transcript poll.
	Context *contextUsage `json:"context,omitempty"`
	// Branch is the session's start branch (sessionMeta.Branch). CurrentBranch is the
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
}

func tmuxName(name string) string { return tmuxPrefix + name }

// sessionMeta records how to (re)launch a session. tmux destroys a session when
// its program exits (e.g. the user quits claude), losing the kind/dir/model we
// need to relaunch. We persist it in the home volume so the session stays listed
// and clicking it re-runs claude --resume in the SAME session id (derived from
// dir+name). Home survives Stop→Start, so a stopped session remains listed and
// resumable across a Workspace restart (claude --resume reads the jsonl, also
// persisted). The dir is denylisted in the file browser. "作り直す"(recreate)
// wipes home, intentionally clearing sessions too.
type sessionMeta struct {
	Name  string `json:"name"`
	Dir   string `json:"dir"`
	Model string `json:"model"`
	Kind  string `json:"kind"`
	Title string `json:"title"` // user-supplied display title (optional); "" = auto
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
	// ForkFrom is the SOURCE session's sid this session was forked from (claude
	// only). It only affects the FIRST launch: buildSessionProgram then runs
	// `claude --resume <ForkFrom> --fork-session --session-id <ownsid>`, which copies
	// the source history into this session's own jsonl. Once that jsonl exists,
	// later launches resume normally and ForkFrom is ignored — so a restart never
	// re-forks. Empty for non-forked sessions.
	ForkFrom string `json:"forkFrom,omitempty"`
	// SSM holds the (non-secret) coordinates for a kind=ssm session: which instance,
	// run-as document, region, and the SSO profile to authenticate with. Persisted so
	// a relaunch regenerates ~/.aws/config and re-runs `aws sso login` (if the cached
	// token expired) before start-session. No AWS credentials are stored anywhere —
	// the aws CLI obtains them via SSO at launch and caches them in the home volume.
	SSM *ssmMeta `json:"ssm,omitempty"`
}

// ssmMeta is the persisted, non-secret description of an SSM login target.
type ssmMeta struct {
	Profile   string `json:"profile"`   // ~/.aws/config profile name (derived from alias)
	Target    string `json:"target"`    // EC2 instance id (i-...)
	Document  string `json:"document"`  // run-as SSM document ("" = default shell)
	Region    string `json:"region"`    // instance region ("" = profile default)
	StartURL  string `json:"startUrl"`  // SSO access-portal start URL ("" = use existing ~/.aws)
	SSORegion string `json:"ssoRegion"` // SSO region
	AccountID string `json:"accountId"` // SSO account id
	RoleName  string `json:"roleName"`  // SSO permission-set role name
}

// stoppedTTL is how long a stopped (exited) session stays listed/resumable before
// it is pruned. Configurable; default 7d (metas now persist across Stop→Start, so
// the window spans restarts). A session running at shutdown is marked stopped on
// the next list after restart, starting its TTL then.
func stoppedTTL() time.Duration {
	if v := os.Getenv("AF_SESSION_STOPPED_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 7 * 24 * time.Hour
}

// sessionDisplay derives a human-readable session name, mirroring the Console's
// displayName (lib/sessionview.ts): the user title if set; else a claude session's
// --name label (minus the "[AF] " tag); else "{repo}@MMDD-HHMM". The random slug
// (Name) is never surfaced alone — it's an opaque id users don't recognize, so callers
// (e.g. the Fleet Operator) should report Display, not Name.
func sessionDisplay(m sessionMeta) string {
	if m.Title != "" {
		return m.Title
	}
	if m.Label != "" {
		return strings.TrimLeft(strings.TrimPrefix(m.Label, "[AF]"), " ")
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

// wireSession builds the API representation from a meta and liveness.
func wireSession(m sessionMeta, alive bool) Session {
	started := ""
	if m.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, m.CreatedAt); err == nil {
			started = t.Local().Format("01/02 15:04")
		}
	}
	// The live-dependent fields (state / remote URL / context / resumable / bg-busy)
	// diverge by kind — the agent computes them (see wireLive per implementation).
	li := agentOf(m.Kind).wireLive(m, alive)
	return Session{
		Name: m.Name, Tmux: tmuxName(m.Name), Dir: m.Dir, Kind: m.Kind,
		Repo: m.Repo, Title: m.Title, Display: sessionDisplay(m), Color: m.Color, Label: m.Label,
		Started: started, CreatedAt: m.CreatedAt, Branch: m.Branch,
		RemoteUrl: li.remoteURL, State: li.state, Alive: alive, Resumable: li.resumable,
		BackgroundBusy: li.backgroundBusy, Context: li.context,
	}
}

// dirExists reports whether p is an existing directory.
func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// remoteSessionURL derives the claude.ai Remote Control page for sid from its
// jsonl "bridge-session" line (written when RC connects). The web URL is
// "…/code/session_<bridgeSessionId without the cse_ prefix>". We read only the
// head of the log (the bridge line is written at session start) to stay cheap on
// the polled list. Returns "" when there is no bridge (RC off / not yet connected).
func remoteSessionURL(sid string) string {
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

// dirInfo is a working copy's current branch + worktree flag, cached per dir.
type dirInfo struct {
	branch   string
	worktree bool
}

// annotateSessions enriches each session from its working copy: Worktree (is it a
// linked worktree) and BranchDrift/CurrentBranch (was it switched off its start
// branch). info resolves a dir once and is cached, so N sessions sharing a working
// copy cost a single git call. Drift needs a recorded start branch; worktree does not.
// Split from the git/tmux plumbing so it is unit-testable.
func annotateSessions(sessions []Session, info func(string) dirInfo) {
	cache := map[string]dirInfo{}
	for i := range sessions {
		s := &sessions[i]
		if s.Dir == "" {
			continue
		}
		v, ok := cache[s.Dir]
		if !ok {
			v = info(s.Dir)
			cache[s.Dir] = v
		}
		s.Worktree = v.worktree
		if s.Branch != "" && v.branch != "" && v.branch != s.Branch {
			s.BranchDrift = true
			s.CurrentBranch = v.branch
		}
	}
}

// sessionLabelFor builds the claude --name for a session. When the user supplied a
// title it's "[AF] {title}"; otherwise it falls back to the auto default
// "[AF] {repo} @MMDD-HHMM" where {repo} is the working dir's basename and the time is
// the workspace's local time (the entrypoint exports TZ from the per-user timezone
// setting, default JST). The "[AF] " tag identifies Agent-Fleet-launched sessions in
// the claude.ai Remote Control picker. Computed at create/recreate and stored in the
// meta so relaunch keeps the same name.
func sessionLabelFor(dir, title string) string {
	if title != "" {
		return "[AF] " + title
	}
	return fmt.Sprintf("[AF] %s @%s", filepath.Base(dir), time.Now().Format("0102-1504"))
}

// cleanTitle trims and validates a user-supplied display title. It rejects control
// characters (which would corrupt the tmux title / claude --name) and caps the length.
// An empty title is valid (the session uses the auto default label). Returns ok=false
// only for an over-long or control-laden title.
func cleanTitle(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > 80 {
		return "", false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return s, true
}

// forkTitle derives a fork's display title from its source: the source's own title
// when set, else its stripped label (the auto "{repo} @time"), suffixed " (fork)".
func forkTitle(src sessionMeta) string {
	base := src.Title
	if base == "" {
		base = strings.TrimPrefix(src.Label, "[AF] ")
	}
	return strings.TrimSpace(base + " (fork)")
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
