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
	Alive     bool   `json:"alive"`    // true = live tmux session; false = stopped (resumable)
}

func tmuxName(name string) string { return tmuxPrefix + name }

// sessionMeta records how to (re)launch a session. tmux destroys a session when
// its program exits (e.g. the user quits claude), losing the kind/dir/model we
// need to relaunch. We persist it per-container under /tmp so the session stays
// listed and clicking it re-runs claude --resume in the SAME session id (derived
// from dir+name) instead of falling back to a bare shell. Lifecycle matches tmux:
// /tmp clears on container restart, exactly when tmux sessions are lost too.
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
// it is pruned. Configurable; default 24h. Container restart clears /tmp anyway.
func stoppedTTL() time.Duration {
	if v := os.Getenv("AF_SESSION_STOPPED_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 24 * time.Hour
}

// wireSession builds the API representation from a meta and liveness.
func wireSession(m sessionMeta, alive bool) Session {
	started := ""
	if m.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, m.CreatedAt); err == nil {
			started = t.Local().Format("01/02 15:04")
		}
	}
	return Session{
		Name: m.Name, Tmux: tmuxName(m.Name), Dir: m.Dir, Kind: m.Kind,
		Repo: m.Repo, Label: m.Label, Started: started, CreatedAt: m.CreatedAt, Alive: alive,
	}
}

func sessionsMetaDir() string { return envOr("AF_SESSIONS_DIR", "/tmp/af-sessions") }

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
	args := []string{"new-session", "-d", "-s", tn, "-c", m.Dir}
	var program string
	if m.Kind == "shell" {
		program = "bash -l"
	} else {
		if tok := readClaudeToken(); tok != "" {
			args = append(args, "-e", "CLAUDE_CODE_OAUTH_TOKEN="+tok)
		}
		program = buildSessionProgram(sessionUUID(m.Dir, m.Name), m.Model, m.Label)
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
	// Stable order: newest first by creation time.
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].CreatedAt > sessions[j].CreatedAt })
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
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
	if kind != "shell" {
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
	_, hadMeta := readSessionMeta(name)
	live := tmuxHasSession(tn)
	if !live && !hadMeta {
		writeErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
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

// sessionLabel builds the claude --name for a session: "[AF] {repo} @MMDD-HHMM"
// where {repo} is the working dir's basename and the time is the workspace's local
// time (the entrypoint exports TZ from the per-user timezone setting, default JST).
// Computed once at create and stored in the meta so relaunch keeps the same name.
func sessionLabel(dir string) string {
	return fmt.Sprintf("[AF] %s @%s", filepath.Base(dir), time.Now().Format("0102-1504"))
}

// sessionJSONLExists reports whether a conversation log for sid is on disk.
// It must look where claude actually stores projects — claudeConfigDir()
// (CLAUDE_CONFIG_DIR when set, P3-5 段2), NOT a hardcoded ~/.claude. Otherwise a
// resumable session is missed and we hand claude `--session-id <existing>`, which
// claude rejects with "Session ID is already in use" and exits immediately.
func sessionJSONLExists(sid string) bool {
	matches, _ := filepath.Glob(filepath.Join(claudeConfigDir(), "projects", "*", sid+".jsonl"))
	return len(matches) > 0
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
