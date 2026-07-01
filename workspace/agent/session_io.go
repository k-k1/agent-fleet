package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Programmatic session I/O for the MCP drive tools (docs/decisions/0006, P3-6 E).
// A user's own Claude (via the CP /mcp endpoint) drives the claude sessions in
// their Workspace: send a prompt, poll status, read the reply. Built on the
// existing primitives — tmux send-keys, the session-status hooks (working|idle|
// question), and the claude jsonl transcript — so this is the only new Agent code.

// handleSessionInput (POST /sessions/{name}/input {prompt}) types a prompt into a
// session and submits it. Returns immediately; the caller polls /status then
// /output for the reply.
func handleSessionInput(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	// Body is either {prompt} (type text + Enter) or {keys:[...]} (send named keys —
	// used to drive the AskUserQuestion modal: Down/Space/Enter navigation, which
	// free text can't do because a typed answer submits the whole tool at once).
	var body struct {
		Prompt string   `json:"prompt"`
		Keys   []string `json:"keys"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_body", "invalid JSON body")
		return
	}
	if len(body.Keys) == 0 && strings.TrimSpace(body.Prompt) == "" {
		writeErr(w, http.StatusBadRequest, "empty_prompt", "prompt or keys is required")
		return
	}
	tn := tmuxName(name)
	if !tmuxHasSession(tn) {
		writeErr(w, http.StatusConflict, "not_running", "session is not running; start it first")
		return
	}
	// Resolve the active pane id. send-keys takes a target-PANE, where tmux's "="
	// exact-session prefix is read literally ("can't find pane: =claude_x"); a
	// globally-unique pane id (%N) is unambiguous and avoids tmux's prefix matching.
	pane := sessionPaneID(tn)
	if pane == "" {
		writeErr(w, http.StatusInternalServerError, "no_pane", "could not resolve session pane")
		return
	}
	if len(body.Keys) > 0 {
		// Named-key navigation. Send one at a time with a small gap so the TUI can
		// re-render between keys (e.g. after Enter advances to the next question page).
		for _, k := range body.Keys {
			if !allowedKey(k) {
				writeErr(w, http.StatusBadRequest, "bad_key", "unsupported key: "+k)
				return
			}
		}
		for i, k := range body.Keys {
			if out, err := exec.Command("tmux", "send-keys", "-t", pane, k).CombinedOutput(); err != nil {
				writeErr(w, http.StatusInternalServerError, "tmux_failed", string(out))
				return
			}
			if i < len(body.Keys)-1 {
				time.Sleep(45 * time.Millisecond)
			}
		}
		markSessionWorking(name)
		writeJSON(w, http.StatusOK, map[string]any{"sent": name})
		return
	}
	// Send the prompt literally (-l: no key-name interpretation), then Enter to submit.
	if out, err := exec.Command("tmux", "send-keys", "-t", pane, "-l", body.Prompt).CombinedOutput(); err != nil {
		writeErr(w, http.StatusInternalServerError, "tmux_failed", string(out))
		return
	}
	if out, err := exec.Command("tmux", "send-keys", "-t", pane, "Enter").CombinedOutput(); err != nil {
		writeErr(w, http.StatusInternalServerError, "tmux_failed", string(out))
		return
	}
	markSessionWorking(name)
	writeJSON(w, http.StatusOK, map[string]any{"sent": name})
}

// markSessionWorking optimistically marks the session working so a poll immediately
// after a send doesn't read a stale idle before claude's UserPromptSubmit hook fires.
func markSessionWorking(name string) {
	meta, ok := readSessionMeta(name)
	if !ok {
		return
	}
	sid := sessionUUID(meta.Dir, name)
	_ = os.MkdirAll(sessionStatusDir(), 0o700)
	b, _ := json.Marshal(sessionStatus{State: "working", TS: time.Now().Format(time.RFC3339)})
	_ = os.WriteFile(sessionStatusPath(sid), b, 0o600)
}

// allowedKey is the whitelist of tmux key names the Console may send to drive a TUI
// (the AskUserQuestion modal): navigation + confirm, nothing that could run a command.
func allowedKey(k string) bool {
	switch k {
	case "Up", "Down", "Left", "Right", "Enter", "Space", "Escape", "Tab", "BSpace", "Home", "End":
		return true
	}
	return false
}

// sessionPaneID returns the active pane id (e.g. "%0") of a session's current
// window, or "" if none. Uses the "=" exact target for list-panes (a target-
// SESSION context, where "=" is honored), then returns the active pane.
func sessionPaneID(tn string) string {
	out, err := exec.Command("tmux", "list-panes", "-t", exactT(tn), "-F", "#{pane_active} #{pane_id}").Output()
	if err != nil {
		return ""
	}
	first := ""
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(ln)
		if len(f) != 2 {
			continue
		}
		if first == "" {
			first = f[1]
		}
		if f[0] == "1" {
			return f[1]
		}
	}
	return first // fall back to the first pane if none flagged active
}

// handleSessionStatus (GET /sessions/{name}/status) returns the session's live
// state for the drive poll loop: working | idle | question, plus alive.
func handleSessionStatus(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	meta, ok := readSessionMeta(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	alive := tmuxHasSession(tmuxName(name))
	state := "stopped"
	if alive {
		state = "idle"
		if st, ok := readSessionStatus(sessionUUID(meta.Dir, name)); ok {
			state = st.State
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "kind": meta.Kind, "alive": alive, "status": state})
}

// handleSessionOutput (GET /sessions/{name}/output?since=<cursor>) returns the
// session's assistant text appended since the cursor, plus a new cursor and the
// current status. Phase 1: claude only (its jsonl transcript). cursor is a line
// index into the transcript.
func handleSessionOutput(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	meta, ok := readSessionMeta(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	alive := tmuxHasSession(tmuxName(name))
	state := "stopped"
	if alive {
		state = "idle"
		if st, ok := readSessionStatus(sessionUUID(meta.Dir, name)); ok {
			state = st.State
		}
	}
	if meta.Kind != "claude" {
		writeErr(w, http.StatusBadRequest, "unsupported_kind", "output is available for claude sessions only (phase 1)")
		return
	}
	since := 0
	if v := r.URL.Query().Get("since"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			since = n
		}
	}
	sid := sessionUUID(meta.Dir, name)
	lines := transcriptLines(sid)
	var sb strings.Builder
	for i := since; i < len(lines); i++ {
		if t := assistantText(lines[i]); t != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(t)
		}
		if sb.Len() > 1<<20 { // cap at 1 MiB
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": name, "output": sb.String(), "cursor": len(lines),
		"status": state, "alive": alive,
	})
}

// transcriptLines reads the session's jsonl conversation log as raw lines. claude
// stores one JSON event per line; we treat the line count as the cursor.
func transcriptLines(sid string) [][]byte {
	paths := jsonlPaths(sid)
	if len(paths) == 0 {
		return nil
	}
	b, err := os.ReadFile(paths[0])
	if err != nil {
		return nil
	}
	var out [][]byte
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, []byte(ln))
		}
	}
	return out
}

// assistantText extracts the concatenated text blocks from an assistant event
// line (skips user/tool/bookkeeping lines).
func assistantText(line []byte) string {
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &ev) != nil || ev.Type != "assistant" {
		return ""
	}
	var sb strings.Builder
	for _, c := range ev.Message.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}
