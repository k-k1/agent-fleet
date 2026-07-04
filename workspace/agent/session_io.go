package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"sort"
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
				time.Sleep(90 * time.Millisecond)
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
	// Pause before Enter so the TUI finishes ingesting the pasted text first. codex's
	// (and opencode's) input widget can drop an Enter that arrives while it's still
	// consuming the paste — the symptom being a prompt that sits in the composer until
	// the user presses Enter again in the terminal. claude tolerates back-to-back, but a
	// short beat is harmless there too.
	time.Sleep(inputSubmitDelay(name))
	if out, err := exec.Command("tmux", "send-keys", "-t", pane, "Enter").CombinedOutput(); err != nil {
		writeErr(w, http.StatusInternalServerError, "tmux_failed", string(out))
		return
	}
	markSessionWorking(name)
	writeJSON(w, http.StatusOK, map[string]any{"sent": name})
}

// inputSubmitDelay is how long to wait between typing a prompt and sending Enter, per
// kind. codex/opencode need a beat so their input widget doesn't drop the Enter mid-
// paste (a prompt left un-submitted in the composer); claude submits reliably back-to-
// back so it gets a token pause. Tunable via AGENT_INPUT_SUBMIT_DELAY_MS.
func inputSubmitDelay(name string) time.Duration {
	ms := 200
	if meta, ok := readSessionMeta(name); ok && meta.Kind == kindClaude {
		ms = 20
	}
	if v := os.Getenv("AGENT_INPUT_SUBMIT_DELAY_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 5000 {
			ms = n
		}
	}
	return time.Duration(ms) * time.Millisecond
}

// markSessionWorking optimistically marks the session working so a poll immediately
// after a send doesn't read a stale idle before claude's UserPromptSubmit hook fires.
func markSessionWorking(name string) {
	meta, ok := readSessionMeta(name)
	if !ok {
		return
	}
	persistSessionStatus(sessionUUID(meta.Dir, name), "working")
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
	state := driveState(meta, alive, true)
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
	// /output opts out of the idle-heal (heal=false) to preserve its historical behavior.
	state := driveState(meta, alive, false)
	if !agentOf(meta.Kind).caps().canTranscript {
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

// jsonlByMtime returns sid's conversation logs newest-first. claude can leave more
// than one <sid>.jsonl under projects/* (a cwd change, a CLAUDE_CONFIG_DIR switch,
// or a stale log from an earlier run all produce siblings under different project
// dirs). glob order is lexical, so paths[0] can be an OLD file that never grows —
// the chat then freezes on stale content. The live log is the most recently written
// one, so we sort by mtime and read that.
func jsonlByMtime(sid string) []string {
	paths := jsonlPaths(sid)
	sort.SliceStable(paths, func(i, j int) bool {
		return jsonlMtime(paths[i]).After(jsonlMtime(paths[j]))
	})
	return paths
}

func jsonlMtime(p string) time.Time {
	fi, err := os.Stat(p)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// transcriptRead reads the session's live jsonl as raw lines — one JSON event per
// line, the line count being the cursor — and returns the chosen path plus every
// matching path (for the /messages diagnostics).
//
// It prefers the NEWEST log that actually holds a conversation. A session commonly
// has sibling <sid>.jsonl files: the real transcript, plus stubs (a Remote Control
// "bridge-session", a lone summary) that can carry a NEWER mtime — while a workflow
// runs, a bridge stub may be touched more recently than the main log. Reading a stub
// would show an empty chat, so we skip stubs and fall back to the newest file only
// when none has real turns yet.
func transcriptRead(sid string) (lines [][]byte, path string, matched []string) {
	matched = jsonlByMtime(sid)
	if len(matched) == 0 {
		return nil, "", nil
	}
	var fallback [][]byte
	fallbackPath := matched[0]
	for i, p := range matched {
		ls := readJSONLLines(p)
		if i == 0 {
			fallback = ls
		}
		if jsonlHasConversation(ls) {
			return ls, p, matched
		}
	}
	return fallback, fallbackPath, matched
}

// readJSONLLines reads a jsonl file into its non-empty raw lines.
func readJSONLLines(p string) [][]byte {
	b, err := os.ReadFile(p)
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

// jsonlHasConversation reports whether the lines include a real user/assistant turn
// (not just bookkeeping) — the per-file form of jsonlResumable, so we can skip stubs.
func jsonlHasConversation(lines [][]byte) bool {
	for _, ln := range lines {
		if bytesContains(ln, `"type":"user"`) || bytesContains(ln, `"type":"assistant"`) {
			return true
		}
	}
	return false
}

// transcriptLines is the lines-only view (the /output MCP poll doesn't need the
// source path); both share the newest-file selection above.
func transcriptLines(sid string) [][]byte {
	lines, _, _ := transcriptRead(sid)
	return lines
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
