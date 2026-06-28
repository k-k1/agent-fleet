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
	Kind      string `json:"kind"`     // "claude" | "shell"
	Repo      string `json:"repo"`     // working dir basename (display)
	Label     string `json:"label"`    // claude --name display name (claude only)
	Started   string `json:"started"`  // "01/02 15:04" local time, for the list
	CreatedAt string `json:"createdAt"`// RFC3339
	RemoteUrl string `json:"remoteUrl"`// claude.ai Remote Control URL, when RC is bridged
	State     string `json:"state"`    // claude live state: working | idle | question | ""
	Alive     bool   `json:"alive"`    // true = live tmux session; false = stopped
	Resumable bool   `json:"resumable"`// false = stopped claude whose working dir is gone
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
	Name      string `json:"name"`
	Dir       string `json:"dir"`
	Model     string `json:"model"`
	Kind      string `json:"kind"`
	Label     string `json:"label"`     // claude --name (display); set once at create
	Repo      string `json:"repo"`      // working dir basename
	CreatedAt string `json:"createdAt"` // RFC3339, set at create
	StoppedAt string `json:"stoppedAt"` // RFC3339, set lazily when first seen exited; "" while live
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

// wireSession builds the API representation from a meta and liveness.
func wireSession(m sessionMeta, alive bool) Session {
	started := ""
	if m.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, m.CreatedAt); err == nil {
			started = t.Local().Format("01/02 15:04")
		}
	}
	remote, state := "", ""
	resumable := true
	if m.Kind == "claude" {
		sid := sessionUUID(m.Dir, m.Name)
		remote = remoteSessionURL(sid)
		if alive {
			// Default a live claude with no recorded event yet to idle (it sits at
			// the prompt waiting for input). Hook events refine it.
			state = "idle"
			if st, ok := readSessionStatus(sid); ok {
				state = st.State
			}
		} else if !dirExists(m.Dir) {
			// A stopped claude whose working dir was removed (its repo deleted) can't
			// be resumed there; the Console marks it non-resumable (archive only).
			resumable = false
		}
	} else if m.Kind == "opencode" {
		if alive {
			// State comes from the bundled opencode plugin (keyed by our sid). No
			// recorded event yet => idle (sitting at the prompt).
			state = "idle"
			if st, ok := readSessionStatus(sessionUUID(m.Dir, m.Name)); ok {
				state = st.State
			}
		} else if !dirExists(m.Dir) {
			// Same as claude: opencode --continue needs its real project dir.
			resumable = false
		}
	}
	return Session{
		Name: m.Name, Tmux: tmuxName(m.Name), Dir: m.Dir, Kind: m.Kind,
		Repo: m.Repo, Label: m.Label, Started: started, CreatedAt: m.CreatedAt,
		RemoteUrl: remote, State: state, Alive: alive, Resumable: resumable,
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

// startSessionTmux launches the detached tmux session for m. For claude it injects
// the OAuth token and builds the resume/new program (buildSessionProgram picks
// --resume once a jsonl exists); for shell it runs a login bash.
func startSessionTmux(m sessionMeta) error {
	tn := tmuxName(m.Name)
	cwd := m.Dir
	dirOK := dirExists(cwd)
	args := []string{"new-session", "-d", "-s", tn, "-c", cwd}
	var program string
	switch m.Kind {
	case "shell":
		// A shell falls back to home if its recorded dir is gone.
		if !dirOK {
			cwd = homeDir()
			args[len(args)-1] = cwd
		}
		program = "bash -l"
	case "opencode":
		// opencode resumes (or starts) in its real project dir; refuse if it's gone.
		if !dirOK {
			return fmt.Errorf("作業フォルダが存在しないため再開できません: %s", m.Dir)
		}
		// AF_SESSION_SID lets the bundled opencode plugin report this session's
		// working/idle state back keyed by OUR deterministic sid (same store claude
		// uses), so wireSession can surface it. Provider API keys are injected as env
		// (ANTHROPIC_API_KEY, …) so opencode authenticates without a plaintext file.
		// The env is prefixed onto the command itself (not tmux -e, which sets only
		// the session environment and does NOT reach the pane's process).
		ocSid := sessionUUID(m.Dir, m.Name)
		envs := append([]string{"AF_SESSION_SID=" + ocSid}, opencodeEnv()...)
		program = buildOpencodeProgram(m.Model, envs, readOpencodeSid(ocSid))
	default:
		// A claude session must launch in its real working dir: if the dir is gone
		// (its repo was deleted) we refuse rather than resume the conversation in an
		// unrelated cwd. wireSession reports this as non-resumable.
		if !dirOK {
			return fmt.Errorf("作業フォルダが存在しないため再開できません: %s", m.Dir)
		}
		// Pre-trust the launch dir so claude doesn't stall on the folder-trust
		// dialog (not skippable via --dangerously-skip-permissions).
		ensureFolderTrusted(cwd)
		sid := sessionUUID(m.Dir, m.Name)
		// A jsonl can exist yet hold no real conversation — e.g. only a Remote
		// Control "bridge-session" line when RC connected but nothing was said.
		// claude --resume then dies with "No conversation found". Drop such a stub
		// so buildSessionProgram starts fresh (--session-id) instead of resuming.
		if !jsonlResumable(sid) {
			for _, p := range jsonlPaths(sid) {
				_ = os.Remove(p)
			}
		}
		program = buildSessionProgram(sid, m.Model, m.Label)
		// Inject the Claude OAuth token by prefixing it onto the command. tmux's -e
		// only sets the session environment, which does NOT reach the pane's process,
		// so a session on a fresh home (no stored ~/.claude credentials) would fail
		// to authenticate. Prefixing guarantees claude's process receives it.
		if tok := readClaudeToken(); tok != "" {
			program = "CLAUDE_CODE_OAUTH_TOKEN=" + shellQuote(tok) + " " + program
		}
	}
	args = append(args, program)
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

// ensureSessionTmux (re)creates the tmux session from its recorded meta when it is
// not currently alive — used on attach so a clicked-but-exited session relaunches
// claude rather than the default shell. Reports whether a meta was found.
func ensureSessionTmux(name string) bool {
	if tmuxHasSession(tmuxName(name)) {
		return true
	}
	m, ok := readSessionMeta(name)
	if !ok {
		return false
	}
	_ = startSessionTmux(m)
	return true
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

	live := map[string]bool{}
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err == nil { // no server / no sessions => err; treat as empty
		for _, tn := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if tn == "" || !strings.HasPrefix(tn, tmuxPrefix) {
				continue
			}
			live[strings.TrimPrefix(tn, tmuxPrefix)] = true
		}
	}

	now := time.Now()
	ttl := stoppedTTL()
	sessions := []Session{}
	for name, m := range metas {
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
	// Stable order: newest first by creation time.
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].CreatedAt > sessions[j].CreatedAt })
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

// paneKind sniffs a session's kind from its tmux pane start command, used for
// orphan sessions that have no meta. Best-effort; defaults to shell.
func paneKind(name string) string {
	out, err := exec.Command("tmux", "list-panes", "-t", tmuxName(name), "-F", "#{pane_start_command}").Output()
	if err != nil {
		return "shell"
	}
	s := string(out)
	switch {
	case strings.Contains(s, "opencode"):
		return "opencode"
	case strings.Contains(s, "claude"):
		return "claude"
	default:
		return "shell"
	}
}

type createReq struct {
	Name  string `json:"name"`
	Dir   string `json:"dir"`
	Model string `json:"model"`
	Kind  string `json:"kind"` // "claude" (default) | "shell"
	// Optional clone-then-start: when remote_url is set, the repo is cloned
	// (or reused) under ~/repos and its path becomes the session CWD, ignoring dir.
	RemoteURL string `json:"remote_url"`
	Branch    string `json:"branch"`
}

// handleCreateSession launches a claude session inside a detached tmux session.
func handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if !nameRe.MatchString(req.Name) {
		writeErr(w, http.StatusBadRequest, "bad_name", "name must match [A-Za-z0-9_-]{1,40}")
		return
	}
	// Clone-then-start: ensure the repo exists and use it as the working dir.
	if strings.TrimSpace(req.RemoteURL) != "" {
		dir, err := ensureRepo(req.RemoteURL, req.Branch)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "clone_failed", err.Error())
			return
		}
		req.Dir = dir
	}
	if req.Dir == "" {
		req.Dir = os.Getenv("HOME")
	}
	if fi, err := os.Stat(req.Dir); err != nil || !fi.IsDir() {
		writeErr(w, http.StatusBadRequest, "bad_dir", "dir does not exist: "+req.Dir)
		return
	}

	tn := tmuxName(req.Name)
	// Block only on a live session; a recorded-but-exited one is overwritten so the
	// user can recreate a session they previously quit.
	if tmuxHasSession(tn) {
		writeErr(w, http.StatusConflict, "exists", "session already running: "+req.Name)
		return
	}

	kind := req.Kind
	switch kind {
	case "shell", "opencode":
		// supported as-is
	default:
		kind = "claude"
	}
	label := ""
	if kind == "claude" {
		label = sessionLabel(req.Dir)
	}
	meta := sessionMeta{
		Name: req.Name, Dir: req.Dir, Model: req.Model, Kind: kind, Label: label,
		Repo: filepath.Base(req.Dir), CreatedAt: time.Now().Format(time.RFC3339),
	}
	if err := startSessionTmux(meta); err != nil {
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
		if out, err := exec.Command("tmux", "kill-session", "-t", tn).CombinedOutput(); err != nil {
			writeErr(w, http.StatusInternalServerError, "tmux_failed", fmt.Sprintf("%v: %s", err, out))
			return
		}
	}
	removeSessionMeta(name)
	writeJSON(w, http.StatusOK, map[string]any{"stopped": name})
}

// handleRecreateSession discards the session's conversation and starts it fresh in
// the same slot (name/dir/model). This is distinct from resume: the deterministic
// sid is reused but its jsonl is deleted first, so claude opens a NEW conversation
// (--session-id) instead of --resume. The display name/time are refreshed.
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
	if tn := tmuxName(name); tmuxHasSession(tn) {
		_ = exec.Command("tmux", "kill-session", "-t", tn).Run()
	}
	// Throw away the past conversation and stale state so the same sid starts clean.
	sid := sessionUUID(m.Dir, m.Name)
	for _, p := range jsonlPaths(sid) {
		_ = os.Remove(p)
	}
	removeSessionStatus(sid)
	// opencode: forget the captured session id so recreate starts a NEW opencode
	// conversation (plain launch) instead of resuming the old one.
	removeOpencodeSid(sid)
	m.CreatedAt = time.Now().Format(time.RFC3339)
	if m.Kind == "claude" {
		m.Label = sessionLabel(m.Dir)
	}
	m.StoppedAt = ""
	if err := startSessionTmux(m); err != nil {
		writeErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
		return
	}
	writeSessionMeta(m)
	writeJSON(w, http.StatusCreated, wireSession(m, true))
}

func tmuxHasSession(tn string) bool {
	return exec.Command("tmux", "has-session", "-t", tn).Run() == nil
}

// buildSessionProgram returns the shell command tmux should run for a session.
// AGENT_SESSION_CMD overrides claude entirely (e.g. "bash") for plumbing tests.
// Otherwise it resumes when a session jsonl already exists, else starts new.
// label, when non-empty, becomes claude's --name (display name shown in the
// Remote Control picker and terminal title), e.g. "[AF] agent-fleet @0627-2115".
func buildSessionProgram(sid, model, label string) string {
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
		return fmt.Sprintf("claude --resume %s %s", sid, flags)
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

// opencode session-id store: maps our deterministic slot sid (AF_SESSION_SID) to the
// opencode session id (ses_…) the plugin captured, so a slot resumes its OWN session.
func opencodeSidDir() string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", "opencode-sid")
}

func readOpencodeSid(sid string) string {
	b, err := os.ReadFile(filepath.Join(opencodeSidDir(), sid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func removeOpencodeSid(sid string) { _ = os.Remove(filepath.Join(opencodeSidDir(), sid)) }

// sessionLabel builds the claude --name for a session: "[AF] {repo} @MMDD-HHMM"
// where {repo} is the working dir's basename and the time is the workspace's local
// time (the entrypoint exports TZ from the per-user timezone setting, default JST).
// Computed once at create and stored in the meta so relaunch keeps the same name.
func sessionLabel(dir string) string {
	return fmt.Sprintf("[AF] %s @%s", filepath.Base(dir), time.Now().Format("0102-1504"))
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
