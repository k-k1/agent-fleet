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
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_body", "invalid JSON body")
		return
	}
	if strings.TrimSpace(body.Prompt) == "" {
		writeErr(w, http.StatusBadRequest, "empty_prompt", "prompt is required")
		return
	}
	tn := tmuxName(name)
	if !tmuxHasSession(tn) {
		writeErr(w, http.StatusConflict, "not_running", "session is not running; start it first")
		return
	}
	// Send the prompt literally (-l: no key-name interpretation), then Enter to
	// submit. exactT(=) avoids tmux's prefix matching hitting a sibling session.
	if out, err := exec.Command("tmux", "send-keys", "-t", exactT(tn), "-l", body.Prompt).CombinedOutput(); err != nil {
		writeErr(w, http.StatusInternalServerError, "tmux_failed", string(out))
		return
	}
	if out, err := exec.Command("tmux", "send-keys", "-t", exactT(tn), "Enter").CombinedOutput(); err != nil {
		writeErr(w, http.StatusInternalServerError, "tmux_failed", string(out))
		return
	}
	// Optimistically mark working so a poll immediately after send doesn't read a
	// stale idle before claude's UserPromptSubmit hook fires.
	if meta, ok := readSessionMeta(name); ok {
		sid := sessionUUID(meta.Dir, name)
		_ = os.MkdirAll(sessionStatusDir(), 0o700)
		b, _ := json.Marshal(sessionStatus{State: "working", TS: time.Now().Format(time.RFC3339)})
		_ = os.WriteFile(sessionStatusPath(sid), b, 0o600)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": name})
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
