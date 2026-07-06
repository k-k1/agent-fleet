package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

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
	Color                   string `json:"color"`     // terminal background hue (hex); set at create (SSM host color)
	Label                   string `json:"label"`     // claude --name (display); derived from Title at create/recreate
	Repo                    string `json:"repo"`      // working dir basename
	// Branch is the git branch the working copy (Dir) was on when this session was
	// created/recreated. Compared against Dir's current branch on each list to flag
	// drift — a `git checkout` that slipped past the checkout guard (agent/manual
	// shell inside the session). "" when Dir isn't a git working tree, or for
	// pre-existing sessions minted before this field. Never rewritten after create,
	// so the drift comparison stays meaningful.
	Branch                  string `json:"branch,omitempty"`
	CreatedAt               string `json:"createdAt"` // RFC3339, set at create
	StoppedAt               string `json:"stoppedAt"` // RFC3339, set lazily when first seen exited; "" while live
	Archived                bool   `json:"archived"`  // true = hidden from the active list, restorable (jsonl kept)
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

// sessionsMetaDir lives in the home volume (persists across Stop→Start) under the
// denylisted .config/agent-fleet, so stopped sessions survive a Workspace restart.
func sessionsMetaDir() string {
	return envOr("AF_SESSIONS_DIR", filepath.Join(homeDir(), ".config", "agent-fleet", "sessions"))
}

func sessionMetaPath(name string) string { return filepath.Join(sessionsMetaDir(), name+".json") }

func writeSessionMeta(m sessionMeta) {
	if err := os.MkdirAll(sessionsMetaDir(), 0o700); err != nil {
		return
	}
	if b, err := json.Marshal(m); err == nil {
		_ = os.WriteFile(sessionMetaPath(m.Name), b, 0o600)
	}
}

func readSessionMeta(name string) (sessionMeta, bool) {
	var m sessionMeta
	b, err := os.ReadFile(sessionMetaPath(name))
	if err != nil {
		return m, false
	}
	if json.Unmarshal(b, &m) != nil {
		return m, false
	}
	return m, true
}

func removeSessionMeta(name string) { _ = os.Remove(sessionMetaPath(name)) }

func listSessionMetas() []sessionMeta {
	ents, err := os.ReadDir(sessionsMetaDir())
	if err != nil {
		return nil
	}
	var out []sessionMeta
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if m, ok := readSessionMeta(strings.TrimSuffix(e.Name(), ".json")); ok {
			out = append(out, m)
		}
	}
	return out
}

// ssmConfigPath is the per-session ~/.aws config file for an SSM session. It is
// per-session (not shared) so concurrent SSM sessions to different accounts don't
// clobber each other's AWS_CONFIG_FILE; the SSO token cache stays in the default
// ~/.aws/sso/cache so one `aws sso login` is reused across sessions of the same
// portal. The ".aws" tree is denylisted from the file browser (fs.go).
func ssmConfigPath(name string) string {
	return filepath.Join(homeDir(), ".aws", "af-sessions", name+".config")
}

// writeSSMConfig writes an isolated aws config (sso-session + profile) from the
// non-secret SSM meta. Idempotent — rewritten on every (re)launch. Contains no
// secrets (only the SSO start URL / account / role).
func writeSSMConfig(path string, s ssmMeta) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	region := s.Region
	if region == "" {
		region = s.SSORegion
	}
	ssoName := "af-" + s.Profile
	var b strings.Builder
	fmt.Fprintf(&b, "[sso-session %s]\n", ssoName)
	fmt.Fprintf(&b, "sso_start_url = %s\n", s.StartURL)
	fmt.Fprintf(&b, "sso_region = %s\n", s.SSORegion)
	b.WriteString("sso_registration_scopes = sso:account:access\n\n")
	fmt.Fprintf(&b, "[profile %s]\n", s.Profile)
	fmt.Fprintf(&b, "sso_session = %s\n", ssoName)
	if s.AccountID != "" {
		fmt.Fprintf(&b, "sso_account_id = %s\n", s.AccountID)
	}
	if s.RoleName != "" {
		fmt.Fprintf(&b, "sso_role_name = %s\n", s.RoleName)
	}
	if region != "" {
		fmt.Fprintf(&b, "region = %s\n", region)
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// buildSSMProgram assembles the pane command for an SSM session: refresh SSO creds
// only when the cached token is missing/expired (surfacing the login URL in the
// terminal), then exec start-session. When StartURL is set an isolated aws config is
// generated; otherwise the profile is assumed to exist in the member's own ~/.aws.
func buildSSMProgram(name string, s ssmMeta, force bool) (string, error) {
	var b strings.Builder
	if s.StartURL != "" && s.Profile != "" {
		cfg := ssmConfigPath(name)
		if err := writeSSMConfig(cfg, s); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "export AWS_CONFIG_FILE=%s; ", shellQuote(cfg))
	}
	if s.Profile != "" {
		fmt.Fprintf(&b, "export AWS_PROFILE=%s; ", shellQuote(s.Profile))
	}
	// aws sso login refreshes only when the cached token is missing/expired.
	// --use-device-code forces the device-authorization grant (user_code + verify URL,
	// polled) instead of the default authorization-code+PKCE flow, which spins up a
	// local 127.0.0.1 listener and redirects the browser there — unreachable when the
	// browser is on the user's machine and the CLI runs in this remote container.
	// --no-browser prints the URL instead of trying to open a (nonexistent) browser.
	// Phishing guard: the device-code grant is only safe when the user approves a code
	// they themselves initiated. Warn right before the URL/code appears. force drops the
	// cached-token short-circuit (logout+login) so the user can re-authenticate on demand.
	if force {
		b.WriteString("echo '[Agent Fleet] 再ログインします（自分で開始したこのログインのみ承認してください）'; " +
			"aws sso logout >/dev/null 2>&1; aws sso login --use-device-code --no-browser; ")
	} else {
		b.WriteString("aws sts get-caller-identity >/dev/null 2>&1 || { " +
			"echo '[Agent Fleet] 自分で開始したこのログインのみ承認してください（身に覚えのないコード/URL は入力しない）'; " +
			"aws sso login --use-device-code --no-browser; }; ")
	}
	b.WriteString("exec aws ssm start-session")
	fmt.Fprintf(&b, " --target %s", shellQuote(s.Target))
	if s.Document != "" {
		fmt.Fprintf(&b, " --document-name %s", shellQuote(s.Document))
	}
	if s.Region != "" {
		fmt.Fprintf(&b, " --region %s", shellQuote(s.Region))
	}
	return b.String(), nil
}

// startSessionTmux launches the detached tmux session for m. For claude it injects
// the OAuth token and builds the resume/new program (buildSessionProgram picks
// --resume once a jsonl exists); for shell it runs a login bash.
func startSessionTmux(m sessionMeta, ssmForce bool) error {
	// The kind decides the pane program and launch dir; the agent builds both.
	plan, err := agentOf(m.Kind).buildLaunch(m, launchOpts{ssmForce: ssmForce})
	if err != nil {
		return err
	}
	// Inject the current toolchain selection (JAVA_HOME / node / TZ) so a Console
	// change applies to this freshly-launched session without a Stop→Start. tmux
	// runs the pane command via /bin/sh -c, so the export prefix takes effect.
	program := toolchainShellPrefix() + plan.program
	args := []string{"new-session", "-d", "-s", tmuxName(m.Name), "-c", plan.cwd, program}
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

// ensureSessionTmux (re)creates the tmux session from its recorded meta when it is
// not currently alive — used on attach so a clicked-but-exited session relaunches
// claude rather than the default shell. Reports whether a meta was found.
func ensureSessionTmux(name string, ssmForce bool) bool {
	if tmuxHasSession(tmuxName(name)) {
		return true
	}
	m, ok := readSessionMeta(name)
	if !ok {
		return false
	}
	_ = startSessionTmux(m, ssmForce)
	return true
}

// liveSessionNames returns the set of currently-running claude_* tmux session
// slugs. A missing tmux server / no sessions yields an error, which we treat as
// "none live". Shared by the session list and the branch-switch guard so both
// agree on what "running" means.
func liveSessionNames() map[string]bool {
	live := map[string]bool{}
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return live
	}
	for _, tn := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if tn == "" || !strings.HasPrefix(tn, tmuxPrefix) {
			continue
		}
		live[strings.TrimPrefix(tn, tmuxPrefix)] = true
	}
	return live
}

// liveSessionsInDir returns the display names of running sessions whose cwd is at
// or under dir. Switching branches in dir would swap the working tree beneath
// these processes mid-flight (vanished/rewritten files, stale diffs, edits landing
// on the wrong branch) — the "大惨事" this guards against — so callers refuse the
// operation while this is non-empty. Only LIVE sessions count: a stopped session
// has no process to corrupt (branch drift for those is handled elsewhere). Archived
// sessions are ignored. A subdir cwd still counts because checkout rewrites the
// whole working tree, not just the repo root.
func liveSessionsInDir(dir string) []string {
	return sessionsInDir(listSessionMetas(), liveSessionNames(), dir)
}

// sessionsInDir is the pure core of liveSessionsInDir (tmux/fs kept out so it is
// testable): from metas + the live set, the display names of running, non-archived
// sessions whose cwd equals dir or sits strictly beneath it. The trailing
// PathSeparator on the prefix test is load-bearing — it keeps "/r/foo" from matching
// a sibling "/r/foobar".
func sessionsInDir(metas []sessionMeta, live map[string]bool, dir string) []string {
	var names []string
	for _, m := range metas {
		if m.Archived || !live[m.Name] {
			continue
		}
		if m.Dir == dir || strings.HasPrefix(m.Dir, dir+string(os.PathSeparator)) {
			names = append(names, sessionDisplay(m))
		}
	}
	sort.Strings(names)
	return names
}

// annotateBranchDrift fills BranchDrift/CurrentBranch for any session whose working
// copy has been switched off the branch it started on. curBranch resolves a dir's
// current branch and is cached per dir, so N sessions sharing one working copy cost a
// single git call. Sessions with no recorded start branch (pre-existing, or a non-git
// dir) are left untouched. Split from the git/tmux plumbing so it is unit-testable.
func annotateBranchDrift(sessions []Session, curBranch func(string) string) {
	cache := map[string]string{}
	for i := range sessions {
		s := &sessions[i]
		if s.Branch == "" {
			continue
		}
		cur, ok := cache[s.Dir]
		if !ok {
			cur = curBranch(s.Dir)
			cache[s.Dir] = cur
		}
		if cur != "" && cur != s.Branch {
			s.BranchDrift = true
			s.CurrentBranch = cur
		}
	}
}

// handleListSessions returns the live claude_* tmux sessions.
// We query names and each session's cwd separately rather than packing both
// into one -F line: a tab/control-char delimiter is mangled by some tmux
// builds (e.g. Debian bookworm 3.3a), so a single delimited format is fragile.
func handleListSessions(w http.ResponseWriter, r *http.Request) {
	metas := map[string]sessionMeta{}
	for _, m := range listSessionMetas() {
		metas[m.Name] = m
	}

	live := liveSessionNames()

	now := time.Now()
	ttl := stoppedTTL()
	sessions := []Session{}
	for name, m := range metas {
		if m.Archived {
			continue // hidden from the active list; restorable via the archive modal
		}
		if live[name] {
			// Running: clear any prior stopped mark so resume resets the clock.
			if m.StoppedAt != "" {
				m.StoppedAt = ""
				writeSessionMeta(m)
			}
			sessions = append(sessions, wireSession(m, true))
			continue
		}
		// Stopped (exited): stamp when first noticed, prune once older than the TTL,
		// otherwise keep it listed as resumable.
		if m.StoppedAt == "" {
			m.StoppedAt = now.Format(time.RFC3339)
			writeSessionMeta(m)
		} else if t, e := time.Parse(time.RFC3339, m.StoppedAt); e == nil && now.Sub(t) > ttl {
			removeSessionMeta(name)
			continue
		}
		sessions = append(sessions, wireSession(m, false))
	}
	// Surface ORPHAN sessions: a live claude_* tmux session with no meta. These are
	// invisible to the meta-driven list above, so the auto-namer would reuse their
	// name and handleCreateSession then fails with "session already running" — a
	// confusing dead end (the session can't be seen or archived). List them so they
	// show up, count toward name uniqueness, and can be attached/archived. We can't
	// recover dir/model without a meta; kind is sniffed from the pane command.
	for name := range live {
		if _, ok := metas[name]; ok {
			continue
		}
		sessions = append(sessions, Session{
			Name: name, Tmux: tmuxName(name), Kind: paneKind(name), Repo: name,
			Alive: true, Resumable: true,
		})
	}
	// Flag any session whose working copy was checked out to a different branch than
	// it started on (a checkout that bypassed the guard). One git call per unique dir.
	annotateBranchDrift(sessions, gitCurrentBranch)
	// Stable order: newest first by creation time.
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].CreatedAt > sessions[j].CreatedAt })
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

// paneKind sniffs a session's kind from its tmux pane start command. This is a LAST
// RESORT, used ONLY for orphan sessions that have no meta (handleListSessions); a
// session with a meta always takes the recorded meta.Kind and never reaches here.
// The match is a fragile substring test: a claude/shell pane that merely RAN
// `opencode`/`codex` as a command (or a wrapper whose path contains one of these
// words) would misclassify. Kept because an orphan has no other signal, and the only
// cost is a wrong badge on a session that already lost its meta. Defaults to shell.
func paneKind(name string) string {
	out, err := exec.Command("tmux", "list-panes", "-t", exactT(tmuxName(name)), "-F", "#{pane_start_command}").Output()
	if err != nil {
		return kindShell
	}
	s := string(out)
	switch {
	case strings.Contains(s, "opencode"):
		return kindOpencode
	case strings.Contains(s, "codex"):
		return kindCodex
	case strings.Contains(s, "claude"):
		return kindClaude
	default:
		return kindShell
	}
}

type createReq struct {
	// Name is IGNORED: the server auto-allocates a unique slug as the session's
	// identity. Kept in the wire struct only so older clients that still send it
	// don't error. Title is the optional user-facing display name (→ claude --name).
	Name  string `json:"name"`
	Title string `json:"title"`
	Color string `json:"color"` // terminal background hue (hex); SSM host color, else empty
	Dir   string `json:"dir"`
	Model string `json:"model"`
	Kind  string `json:"kind"` // "claude" (default) | "opencode" | "codex" | "shell"
	// Optional clone-then-start: when remote_url is set, the repo is cloned
	// (or reused) under ~/repos and its path becomes the session CWD, ignoring dir.
	// RepoName overrides the target folder so two branches of the same repo can
	// be cloned side by side (empty => derived from remote_url, the legacy name).
	RemoteURL string `json:"remote_url"`
	Branch    string `json:"branch"`
	RepoName  string `json:"repo_name"`
	// NewBranch, when set, is created off Branch (the base) right after the clone and
	// switched to, so the session starts on a fresh branch. Empty => no new branch.
	NewBranch string `json:"new_branch"`
	// SSM (kind=ssm) coordinates, resolved and forwarded by the Control Plane from a
	// host bookmark (control-plane/ssm.go). No secrets — SSO login happens in-pane.
	SSMProfile   string `json:"ssm_profile"`
	SSMTarget    string `json:"ssm_target"`
	SSMDocument  string `json:"ssm_document"`
	SSMRegion    string `json:"ssm_region"`
	SSOStartURL  string `json:"sso_start_url"`
	SSORegion    string `json:"sso_region"`
	SSOAccountID string `json:"sso_account_id"`
	SSORoleName  string `json:"sso_role_name"`
	// SSMForceLogin: run `aws sso logout` + `aws sso login` unconditionally at launch
	// (skip the cached-token short-circuit) so the user re-authenticates. One-shot.
	SSMForceLogin bool `json:"ssm_force_login"`
}

// handleCreateSession launches a claude session inside a detached tmux session.
func handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	title, ok := cleanTitle(req.Title)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_title", "title is too long (max 80) or contains control characters")
		return
	}
	// Clone-then-start: ensure the repo exists and use it as the working dir.
	if strings.TrimSpace(req.RemoteURL) != "" {
		dir, err := ensureRepo(req.RemoteURL, req.Branch, req.NewBranch, req.RepoName)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "clone_failed", err.Error())
			return
		}
		req.Dir = dir
	}
	if req.Dir == "" {
		req.Dir = homeDir()
	} else if !filepath.IsAbs(req.Dir) {
		// A relative dir (e.g. "projects/x" from the New Session directory picker) is
		// resolved against home, mirroring the fs/tree browser's home-relative paths.
		req.Dir = filepath.Join(homeDir(), req.Dir)
	}
	if fi, err := os.Stat(req.Dir); err != nil || !fi.IsDir() {
		writeErr(w, http.StatusBadRequest, "bad_dir", "dir does not exist: "+req.Dir)
		return
	}

	// Identity is a freshly allocated random slug — NOT the client's name. It (and the
	// sid it derives) can't collide with an archived/pruned session's jsonl, so a new
	// session never accidentally --resumes a past conversation.
	name := allocSessionName(req.Dir)

	kind := normalizeKind(req.Kind)
	label := ""
	if agentOf(kind).caps().usesLabel {
		label = sessionLabelFor(req.Dir, title)
	}
	var ssm *ssmMeta
	if kind == kindSSM {
		if strings.TrimSpace(req.SSMTarget) == "" {
			writeErr(w, http.StatusBadRequest, "bad_ssm", "ssm_target (instance id) is required")
			return
		}
		ssm = &ssmMeta{
			Profile: req.SSMProfile, Target: req.SSMTarget, Document: req.SSMDocument,
			Region: req.SSMRegion, StartURL: req.SSOStartURL, SSORegion: req.SSORegion,
			AccountID: req.SSOAccountID, RoleName: req.SSORoleName,
		}
		if ssm.Region == "" {
			ssm.Region = ssm.SSORegion
		}
	}
	meta := sessionMeta{
		Name: name, Dir: req.Dir, Model: req.Model, Kind: kind, Title: title, Color: req.Color, Label: label,
		Repo: filepath.Base(req.Dir), Branch: gitCurrentBranch(req.Dir),
		CreatedAt: time.Now().Format(time.RFC3339), SSM: ssm,
	}
	if err := startSessionTmux(meta, req.SSMForceLogin); err != nil {
		writeErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
		return
	}
	writeSessionMeta(meta)

	writeJSON(w, http.StatusCreated, wireSession(meta, true))
}

// handleForkSession forks a claude session's conversation into a NEW session
// (POST /sessions/{name}/fork). The fork shares the source's history up to now but
// then diverges independently — the official `claude --fork-session` copies the
// transcript, leaving the source running/intact. Only claude sessions carry a
// resumable jsonl to fork; the fork's first launch (via ForkFrom) materializes its
// own jsonl (see buildSessionProgram), so restarts resume it normally.
func handleForkSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	src, ok := readSessionMeta(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "no_session", "session not found: "+name)
		return
	}
	if !agentOf(src.Kind).caps().canFork {
		writeErr(w, http.StatusBadRequest, "not_claude", "分岐できるのは claude セッションのみです")
		return
	}
	if !dirExists(src.Dir) {
		writeErr(w, http.StatusBadRequest, "bad_dir", "作業フォルダが存在しないため分岐できません")
		return
	}
	srcSid := sessionUUID(src.Dir, name)
	if !jsonlResumable(srcSid) {
		writeErr(w, http.StatusBadRequest, "not_resumable", "分岐できる会話がまだありません")
		return
	}
	forkName := allocSessionName(src.Dir)
	title, _ := cleanTitle(forkTitle(src))
	meta := sessionMeta{
		Name: forkName, Dir: src.Dir, Model: src.Model, Kind: kindClaude, Title: title,
		Label: sessionLabelFor(src.Dir, title), Repo: filepath.Base(src.Dir),
		Branch:    gitCurrentBranch(src.Dir),
		CreatedAt: time.Now().Format(time.RFC3339), ForkFrom: srcSid,
	}
	if err := startSessionTmux(meta, false); err != nil {
		writeErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
		return
	}
	writeSessionMeta(meta)
	writeJSON(w, http.StatusCreated, wireSession(meta, true))
}

// handleStopSession kills the tmux session and forgets its meta so it stops
// appearing in the list. Tolerates an already-exited session (meta only).
func handleStopSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	tn := tmuxName(name)
	meta, hadMeta := readSessionMeta(name)
	live := tmuxHasSession(tn)
	if !live && !hadMeta {
		writeErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if hadMeta {
		removeSessionStatus(sessionUUID(meta.Dir, name))
	}
	if live {
		if out, err := exec.Command("tmux", "kill-session", "-t", exactT(tn)).CombinedOutput(); err != nil {
			writeErr(w, http.StatusInternalServerError, "tmux_failed", fmt.Sprintf("%v: %s", err, out))
			return
		}
	}
	removeSessionMeta(name)
	writeJSON(w, http.StatusOK, map[string]any{"stopped": name})
}

// handleHaltSession stops a RUNNING session into the 停止中 (resumable) state: it
// kills the live tmux but KEEPS the meta visible (Archived stays false), so the row
// stays listed and the user can resume it later (claude --resume). This is the
// button counterpart of quitting in the terminal — distinct from /stop (which also
// forgets the meta = removes it from the list) and /archive (which hides it).
func handleHaltSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	m, ok := readSessionMeta(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	tn := tmuxName(name)
	if !tmuxHasSession(tn) {
		// Already stopped — nothing to do; report the current (stopped) wire.
		writeJSON(w, http.StatusOK, wireSession(m, false))
		return
	}
	// Best-effort: disconnect any active Remote Control bridge before killing the
	// pane, so a later resume's autoconnect registers fresh under the current
	// title instead of resuming the stale one (see disconnectRemoteControl).
	disconnectRemoteControl(name, m)
	if out, err := exec.Command("tmux", "kill-session", "-t", exactT(tn)).CombinedOutput(); err != nil {
		writeErr(w, http.StatusInternalServerError, "tmux_failed", fmt.Sprintf("%v: %s", err, out))
		return
	}
	removeSessionStatus(sessionUUID(m.Dir, name))
	// Stamp StoppedAt now so the prune TTL starts here (handleListSessions would
	// otherwise stamp it on the next poll; doing it here keeps the wire consistent).
	m.StoppedAt = time.Now().Format(time.RFC3339)
	writeSessionMeta(m)
	writeJSON(w, http.StatusOK, wireSession(m, false))
}

// handleArchiveSession hides a session from the active list but KEEPS its meta (and
// jsonl), so it can be restored later. Kills the live tmux session if any. This is
// the non-destructive counterpart to stop (which forgets the meta).
func handleArchiveSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	m, ok := readSessionMeta(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if tn := tmuxName(name); tmuxHasSession(tn) {
		_ = exec.Command("tmux", "kill-session", "-t", exactT(tn)).Run()
	}
	removeSessionStatus(sessionUUID(m.Dir, name))
	m.Archived = true
	writeSessionMeta(m)
	writeJSON(w, http.StatusOK, map[string]any{"archived": name})
}

// handleRestoreSession brings an archived session back into the active list as a
// stopped session (the user clicks it to resume). The conversation (jsonl) is intact.
func handleRestoreSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	m, ok := readSessionMeta(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	m.Archived = false
	m.StoppedAt = "" // re-stamped on next list, resetting the prune clock
	writeSessionMeta(m)
	writeJSON(w, http.StatusOK, wireSession(m, false))
}

// handleListArchived returns archived sessions (for the restore modal).
func handleListArchived(w http.ResponseWriter, r *http.Request) {
	sessions := []Session{}
	for _, m := range listSessionMetas() {
		if m.Archived {
			sessions = append(sessions, wireSession(m, false))
		}
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].CreatedAt > sessions[j].CreatedAt })
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

// handleRecreateSession starts a fresh session in the slot while PRESERVING the old
// one: the old session is archived (hidden from the active list but kept + restorable,
// its jsonl intact), NOT discarded, and a new session (fresh slug/sid, same
// title/dir/model/kind) is minted and pre-launched live. Allocating a new slug (hence
// a new sid) rather than reusing the old id lets the fresh session survive detached
// until the browser attaches (a reused id would exit first), so we pre-launch here
// like create — which lets the Console open it straight into chat. Non-destructive:
// the past conversation stays recoverable from the archive. Returns the new (alive)
// session.
func handleRecreateSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	m, ok := readSessionMeta(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	// Archive the old identity: kill its tmux, clear the live status cache, hide it from
	// the active list. Keep the meta + jsonl (and any captured resume id) so it restores.
	if tn := tmuxName(name); tmuxHasSession(tn) {
		_ = exec.Command("tmux", "kill-session", "-t", exactT(tn)).Run()
	}
	removeSessionStatus(sessionUUID(m.Dir, m.Name))
	m.Archived = true
	writeSessionMeta(m)

	// Fresh identity, same slot. No ForkFrom — recreate means "start empty", not
	// "re-copy the fork source".
	newMeta := sessionMeta{
		Name: allocSessionName(m.Dir), Dir: m.Dir, Model: m.Model, Kind: m.Kind,
		Title: m.Title, Color: m.Color, Repo: m.Repo, Branch: gitCurrentBranch(m.Dir),
		CreatedAt: time.Now().Format(time.RFC3339), SSM: m.SSM,
	}
	if agentOf(newMeta.Kind).caps().usesLabel {
		newMeta.Label = sessionLabelFor(newMeta.Dir, newMeta.Title)
	}
	if err := startSessionTmux(newMeta, false); err != nil {
		// Un-archive the old session so a launch failure doesn't silently drop it from
		// the active list.
		m.Archived = false
		writeSessionMeta(m)
		writeErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
		return
	}
	writeSessionMeta(newMeta)
	writeJSON(w, http.StatusOK, wireSession(newMeta, true))
}

// exactT returns a tmux target that matches NAME exactly. Without the leading '=',
// tmux's -t resolution prefix-matches, so a target like "claude_agent-fleet" would
// match an unrelated "claude_agent-fleet-sh" — wrongly reporting "already running"
// (blocking session creation) or killing the sibling on stop/archive/recreate.
func exactT(tn string) string { return "=" + tn }

func tmuxHasSession(tn string) bool {
	return exec.Command("tmux", "has-session", "-t", exactT(tn)).Run() == nil
}

// sessionAtIdlePrompt reports whether a claude pane is sitting at its ready input
// prompt — used to self-heal a stale status cache (a killed+resumed session, or a
// rejected permission / abandoned question, where no resolving hook fired). The
// mode-cycle footer ("shift+tab to cycle" / "? for shortcuts") shows only at the ready
// prompt; a busy spinner ("esc to interrupt") or any modal ("Enter to select", "Esc to
// cancel", "Do you want to", …) means NOT idle. Best-effort TUI read.
func sessionAtIdlePrompt(name string) bool {
	// capture-pane needs a PANE target; the "=name" exact-SESSION form fails with
	// "can't find pane" (same reason send-keys resolves a %N pane id first).
	pane := sessionPaneID(tmuxName(name))
	if pane == "" {
		return false
	}
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", pane).Output()
	if err != nil {
		return false
	}
	s := string(out)
	for _, busy := range []string{
		"esc to interrupt", "Enter to select", "Esc to cancel", "to approve",
		"Do you want to", "Would you like to proceed", "Ready to submit",
	} {
		if strings.Contains(s, busy) {
			return false
		}
	}
	return strings.Contains(s, "shift+tab to cycle") || strings.Contains(s, "? for shortcuts")
}

// buildSessionProgram returns the shell command tmux should run for a session.
// AGENT_SESSION_CMD overrides claude entirely (e.g. "bash") for plumbing tests.
// Otherwise it resumes when a session jsonl already exists, else starts new.
// label, when non-empty, becomes claude's --name (display name shown in the
// Remote Control picker and terminal title), e.g. "[AF] agent-fleet @0627-2115".
func buildSessionProgram(sid, model, label, forkFrom string) string {
	if override := os.Getenv("AGENT_SESSION_CMD"); override != "" {
		return override
	}
	flags := envOr("AGENT_CLAUDE_FLAGS", "--dangerously-skip-permissions")
	if model != "" {
		flags += " --model " + shellQuote(model)
	}
	if label != "" {
		flags += " --name " + shellQuote(label)
	}
	if sessionJSONLExists(sid) {
		// Already materialized (normal session, or a fork after its first launch):
		// resume our own jsonl. ForkFrom is intentionally ignored here so a restart
		// never re-copies the source.
		return fmt.Sprintf("claude --resume %s %s", sid, flags)
	}
	if forkFrom != "" {
		// First launch of a fork: copy the source conversation into OUR sid via the
		// official --fork-session, pinning the new id with --session-id so it lands
		// exactly on our deterministic jsonl (verified: --session-id sets the fork's
		// id). The source jsonl is left untouched.
		return fmt.Sprintf("claude --resume %s --fork-session --session-id %s %s", forkFrom, sid, flags)
	}
	return fmt.Sprintf("claude --session-id %s %s", sid, flags)
}

// buildOpencodeProgram returns the tmux program for an opencode session. opencode
// keeps its sessions in a local SQLite db (~/.local/share/opencode) and --continue
// resumes the most recent session for the current project, while safely starting a
// new one when none exists — so we always pass it (first launch = fresh, relaunch =
// continue). Auth is the user's own `opencode auth login` (persisted in home), so
// there's no token to inject. Caveat: multiple opencode slots in the SAME dir share
// --continue's "most recent" target.
func buildOpencodeProgram(model string, envs []string, ocid string) string {
	// Prefix env assignments onto the command so the opencode process actually
	// receives them (NAME='value' … opencode). Values are shell-quoted; names are
	// trusted (our sid + validated ALL_CAPS provider env names).
	prefix := ""
	for _, kv := range envs {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		prefix += kv[:i] + "=" + shellQuote(kv[i+1:]) + " "
	}
	parts := []string{"opencode"}
	// Run unattended like claude (--dangerously-skip-permissions) and codex
	// (--dangerously-bypass-…): the container IS the sandbox, so auto-approve every
	// permission prompt (external-dir access, edits, bash) instead of stalling the TUI on
	// an approval the Console user can't answer from chat. --auto approves anything not
	// explicitly denied. Overridable via AGENT_OPENCODE_FLAGS (set to alternate flags).
	parts = append(parts, envOr("AGENT_OPENCODE_FLAGS", "--auto"))
	// Per-slot session: when we've captured this slot's opencode session id (the
	// plugin records it on session.created, keyed by AF_SESSION_SID), resume exactly
	// THAT session. Otherwise launch plain opencode — the TUI creates a fresh session
	// on first message, distinct from other slots. We deliberately do NOT use
	// --continue: it resumes the most-recent session in the project, so two slots in
	// the same dir would collide on one shared conversation.
	if ocid != "" {
		parts = append(parts, "--session", shellQuote(ocid))
	}
	if model != "" {
		// opencode expects provider/model (e.g. anthropic/claude-...); passed through
		// verbatim. The Console only sends this for opencode when explicitly chosen.
		parts = append(parts, "--model", shellQuote(model))
	}
	return prefix + strings.Join(parts, " ")
}

// buildCodexProgram returns the tmux program for a codex session. codex owns its
// auth (~/.codex/auth.json) so no token is injected. It generates its OWN session
// id (no --session-id flag), so we:
//   - inject status hooks via -c, baking OUR slot sid into the hook command so the
//     reported working/idle state is keyed by the slot (codex's hook JSON carries
//     codex's own session_id, which the helper records for resume);
//   - resume exactly THIS slot's codex session when we've captured its id
//     (codexResumeID), else launch plain codex (a fresh session) — mirroring the
//     opencode per-slot model so two codex slots in one dir don't collide.
//
// The bypass flags make codex run unattended like claude's --dangerously-skip-
// permissions: the container IS the sandbox, and we author the injected hooks so
// hook-trust is bypassed too (otherwise the status hooks wouldn't fire).
func buildCodexProgram(model, slotSid, codexResumeID string) string {
	if override := os.Getenv("AGENT_CODEX_CMD"); override != "" {
		return override
	}
	flags := envOr("AGENT_CODEX_FLAGS", "--dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust")
	exe := agentExe()
	// A hook entry as a TOML inline array-of-tables value for `-c hooks.<event>=…`.
	// The command bakes in our slot sid + the "codex" marker so the status helper
	// keys by the slot and captures codex's own session id from the hook's stdin.
	hookFlag := func(event, state string) string {
		cmd := fmt.Sprintf("%s session-status %s %s codex", exe, state, slotSid)
		// codex uses claude's hook schema: hooks.<Event> is an array of entries that
		// each hold a NESTED "hooks" list of {type,command}. A flat [{type,command}]
		// parses without error but never fires, so the nesting is required.
		val := fmt.Sprintf(`hooks.%s=[{hooks=[{type="command",command=%s}]}]`, event, tomlString(cmd))
		return "-c " + shellQuote(val)
	}
	parts := []string{"codex"}
	if codexResumeID != "" {
		parts = append(parts, "resume", shellQuote(codexResumeID))
	}
	parts = append(parts, flags)
	parts = append(parts, hookFlag("UserPromptSubmit", "working"))
	parts = append(parts, hookFlag("Stop", "idle"))
	if model != "" {
		parts = append(parts, "-m", shellQuote(model))
	}
	return strings.Join(parts, " ")
}

// tomlString renders s as a TOML basic string (double-quoted, backslash/quote
// escaped) for embedding in a `-c key=value` override.
func tomlString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
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

// jsonlPaths returns the conversation log file(s) for sid. claude stores them
// under claudeConfigDir()/projects/<project>/<sid>.jsonl (CLAUDE_CONFIG_DIR when
// set, P3-5 段2) — NOT a hardcoded ~/.claude.
func jsonlPaths(sid string) []string {
	m, _ := filepath.Glob(filepath.Join(claudeConfigDir(), "projects", "*", sid+".jsonl"))
	return m
}

// sessionJSONLExists reports whether a conversation log for sid is on disk. When
// it exists buildSessionProgram uses --resume; otherwise --session-id starts new.
// A wrong answer here makes claude exit ("Session ID is already in use").
func sessionJSONLExists(sid string) bool { return len(jsonlPaths(sid)) > 0 }

// jsonlResumable reports whether sid's log holds an actual conversation (a user or
// assistant turn) — not just bookkeeping lines (Remote Control "bridge-session",
// a lone summary, …). claude --resume fails ("No conversation found") on a stub
// log even though the file exists, so we gate resume on real content.
func jsonlResumable(sid string) bool {
	for _, p := range jsonlPaths(sid) {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := string(b)
		if strings.Contains(s, `"type":"user"`) || strings.Contains(s, `"type":"assistant"`) {
			return true
		}
	}
	return false
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
