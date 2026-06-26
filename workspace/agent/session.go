package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// tmux session naming: friendly name "slot01" <-> tmux "claude_slot01".
const tmuxPrefix = "claude_"

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

// Session is the wire representation of a Claude session (one tmux session).
type Session struct {
	Name  string `json:"name"`
	Tmux  string `json:"tmux"`
	Dir   string `json:"dir"`
	Alive bool   `json:"alive"`
}

func tmuxName(name string) string { return tmuxPrefix + name }

// handleListSessions returns the live claude_* tmux sessions.
// We query names and each session's cwd separately rather than packing both
// into one -F line: a tab/control-char delimiter is mangled by some tmux
// builds (e.g. Debian bookworm 3.3a), so a single delimited format is fragile.
func handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions := []Session{}
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err == nil { // no server / no sessions => err; treat as empty
		for _, tn := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if tn == "" || !strings.HasPrefix(tn, tmuxPrefix) {
				continue
			}
			dir := ""
			if p, e := exec.Command("tmux", "display-message", "-p", "-t", tn, "#{pane_current_path}").Output(); e == nil {
				dir = strings.TrimSpace(string(p))
			}
			sessions = append(sessions, Session{
				Name:  strings.TrimPrefix(tn, tmuxPrefix),
				Tmux:  tn,
				Dir:   dir,
				Alive: true,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

type createReq struct {
	Name  string `json:"name"`
	Dir   string `json:"dir"`
	Model string `json:"model"`
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
	if tmuxHasSession(tn) {
		writeErr(w, http.StatusConflict, "exists", "session already running: "+req.Name)
		return
	}

	sid := sessionUUID(req.Dir, req.Name)
	program := buildSessionProgram(sid, req.Model)

	// tmux runs the program via sh -c; it stays alive as the session's process.
	cmd := exec.Command("tmux", "new-session", "-d", "-s", tn, "-c", req.Dir, program)
	if out, err := cmd.CombinedOutput(); err != nil {
		writeErr(w, http.StatusInternalServerError, "tmux_failed", fmt.Sprintf("%v: %s", err, out))
		return
	}

	writeJSON(w, http.StatusCreated, Session{Name: req.Name, Tmux: tn, Dir: req.Dir, Alive: true})
}

// handleStopSession kills the tmux session.
func handleStopSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	tn := tmuxName(name)
	if !tmuxHasSession(tn) {
		writeErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if out, err := exec.Command("tmux", "kill-session", "-t", tn).CombinedOutput(); err != nil {
		writeErr(w, http.StatusInternalServerError, "tmux_failed", fmt.Sprintf("%v: %s", err, out))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stopped": name})
}

func tmuxHasSession(tn string) bool {
	return exec.Command("tmux", "has-session", "-t", tn).Run() == nil
}

// buildSessionProgram returns the shell command tmux should run for a session.
// AGENT_SESSION_CMD overrides claude entirely (e.g. "bash") for plumbing tests.
// Otherwise it resumes when a session jsonl already exists, else starts new.
func buildSessionProgram(sid, model string) string {
	if override := os.Getenv("AGENT_SESSION_CMD"); override != "" {
		return override
	}
	flags := envOr("AGENT_CLAUDE_FLAGS", "--dangerously-skip-permissions")
	modelFlag := ""
	if model != "" {
		modelFlag = " --model " + shellQuote(model)
	}
	if sessionJSONLExists(sid) {
		return fmt.Sprintf("claude --resume %s %s%s", sid, flags, modelFlag)
	}
	return fmt.Sprintf("claude --session-id %s %s%s", sid, flags, modelFlag)
}

// sessionJSONLExists reports whether a conversation log for sid is on disk.
func sessionJSONLExists(sid string) bool {
	home, _ := os.UserHomeDir()
	matches, _ := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", sid+".jsonl"))
	return len(matches) > 0
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
