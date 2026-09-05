package claude

// Building claude's launch command, and locating the jsonl the resume decision rests on.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// envOr is a copy of the identically named helper in package main (too small to be worth
// sharing).
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// nativePeerSettings closes claude's own cross-session channel (ListAgents / SendMessage over
// the UDS `/tmp/cc-socks/<pid>.sock`) for AF sessions (docs/log/58 §58.17 / ADR 0041
// decision 1). Passed to `--settings` as a JSON string.
//
// Why a setting and not env: the effective block used to be the Dockerfile's
// `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` and `DISABLE_TELEMETRY` (measured up to 2.1.226).
// On 2.1.251 traffic gets through with both set — confirmed on a real process — and in that
// gap an inbound message that never passed through AF actually arrived (docs/log/58 §58.16).
// Env cannot be relied on, so the channel is closed explicitly as a setting.
//
// There are two entries because they close opposite DIRECTIONS; one alone does not seal it:
//   - `permissions.deny` (sending side): the two tools DISAPPEAR from the list. They are not
//     called and refused — the model never sees them at all (measured). While visible, the
//     model reaches for a channel with no envelope, no instruction ledger, no rate limit and
//     no delivery guarantee, which is how a misdelivery and an invisible inbound message
//     actually happened.
//   - `crossSessionInbound:"refuse"` (receiving side): stops inbound messages from a claude AF
//     did NOT start (one the user launched by hand). That is the only hole the sending-side
//     deny leaves open.
//
// Why the receiving side alone is not enough (measured): even a refused send returns
// `success:true` to the tool on the spot, and the refusal receipts arrive minutes later, when
// the SENDER next reaches a turn boundary (two of them, 7m03s and 9m49s later, landing in the
// same second). Nothing vanishes silently, but it LOOKS successful at the time, so the sender
// proceeds as if it had arrived and tells the user the message was passed on. Hence deny —
// never showing the tools in the first place — is the primary block, and refuse is a backstop.
//
// `crossSessionInbound` does NOT take effect in `--managed-settings` (measured on 2.1.251).
// `permissions.deny` does, which invites the reading that the policy layer covers both; it
// does not. `--settings` (the flagSettings layer) works for both, so both live here as one.
//
// `--settings` does not REPLACE the existing settings (it only layers on top). The PreToolUse
// hook in the settings.json AF manages (the RTK rewrite) was confirmed to still fire with this
// flag present — get that wrong and every session's hooks and Remote Control settings vanish
// silently.
const nativePeerSettings = `{"permissions":{"deny":["ListAgents","SendMessage"]},"crossSessionInbound":"refuse"}`

// buildProgram returns the shell command tmux should run for a session.
// AGENT_SESSION_CMD overrides claude entirely (e.g. "bash") for plumbing tests.
// Otherwise it resumes when a session jsonl already exists, else starts new.
// label, when non-empty, becomes claude's --name (display name shown in the
// Remote Control picker and terminal title), e.g. "[AF] agent-fleet @0627-2115".
// bypass=false means the permission prompt is NOT skipped (the user's choice per docs/log/76,
// or a plan launch).
func buildProgram(sid, model, effort, mode, label, forkFrom string, bypass bool) string {
	if override := os.Getenv("AGENT_SESSION_CMD"); override != "" {
		return override
	}
	flags := envOr("AGENT_CLAUDE_FLAGS", "--dangerously-skip-permissions")
	if !bypass {
		// --dangerously-skip-permissions forces bypass mode, which is exactly what we
		// are not doing here. Keep bypass available to the IN-SESSION mode cycle
		// (shift+tab) via --allow-…: the flag grants nothing by itself, it only keeps
		// the choice reachable, so a user who hits a wall can lift the gate themselves
		// without relaunching. Plan additionally starts deterministically in Plan
		// through Claude's native permission-mode flag.
		// Replace whole flags (whitespace-delimited tokens): a plain substring replace would
		// corrupt an existing --allow-… into --allow-allow-….
		flags = strings.TrimSpace(strings.ReplaceAll(" "+flags+" ",
			" --dangerously-skip-permissions ", " --allow-dangerously-skip-permissions "))
	}
	// Appended after AGENT_CLAUDE_FLAGS so it cannot be overridden: closing the channel is
	// the point, so no environment variable is left that would disable it.
	flags += " --settings " + session.ShellQuote(nativePeerSettings)
	if mode == "plan" {
		flags += " --permission-mode plan"
	}
	if model != "" {
		flags += " --model " + session.ShellQuote(model)
	}
	if effort != "" {
		flags += " --effort " + session.ShellQuote(effort)
	}
	if label != "" {
		flags += " --name " + session.ShellQuote(label)
	}
	// sid / forkFrom are embedded in the shell command too, so quote them like any other
	// flag value.
	// Resume the id claude is actually writing under, not necessarily our own: when
	// claude restarted itself it dropped --session-id and moved to an id of its own
	// (sid.go). Resuming our slot sid there dies with "No conversation found" and the
	// user silently loses the conversation on every restart.
	if resume := LiveSID(sid); len(rawJSONLPaths(resume)) > 0 {
		// Already materialized (normal session, or a fork after its first launch):
		// resume our own jsonl. ForkFrom is intentionally ignored here so a restart
		// never re-copies the source.
		return fmt.Sprintf("claude --resume %s %s", session.ShellQuote(resume), flags)
	}
	if forkFrom != "" {
		// First launch of a fork: copy the source conversation into OUR sid via the
		// official --fork-session, pinning the new id with --session-id so it lands
		// exactly on our deterministic jsonl (verified: --session-id sets the fork's
		// id). The source jsonl is left untouched.
		return fmt.Sprintf("claude --resume %s --fork-session --session-id %s %s",
			session.ShellQuote(forkFrom), session.ShellQuote(sid), flags)
	}
	return fmt.Sprintf("claude --session-id %s %s", session.ShellQuote(sid), flags)
}

// rawJSONLPaths returns the conversation log file(s) claude stores UNDER THAT EXACT
// id, at ConfigDir()/projects/<project>/<id>.jsonl (CLAUDE_CONFIG_DIR when set,
// P3-5 stage 2) — NOT a hardcoded ~/.claude. Takes the id at face value.
func rawJSONLPaths(id string) []string {
	m, _ := filepath.Glob(filepath.Join(ConfigDir(), "projects", "*", id+".jsonl"))
	return m
}

// jsonlPaths returns the conversation log file(s) for OUR slot sid, following the
// claude-sid ledger when claude restarted itself onto an id of its own (sid.go).
// Everything transcript-shaped goes through here — the mirror, usage, context fill,
// abort detection, background-work detection, the Remote Control URL — so they all
// follow the drift.
func jsonlPaths(sid string) []string {
	return rawJSONLPaths(LiveSID(sid))
}

// SessionJSONLExists reports whether a conversation log for sid is on disk. When
// it exists buildProgram uses --resume (of LiveSID(sid)); otherwise --session-id
// starts new. A wrong answer here makes claude exit ("Session ID is already in use").
func SessionJSONLExists(sid string) bool { return len(jsonlPaths(sid)) > 0 }

// TranscriptSnapshot records the current byte size of each conversation log file for
// sid — the baseline UserTurnAppendedSince compares against. A file that does not
// exist yet simply has no entry (the create path: the log materializes with the first
// turn). Never nil, so callers can distinguish "no baseline support" (nil) from "no
// log yet" (empty map).
func TranscriptSnapshot(sid string) map[string]int64 {
	snap := map[string]int64{}
	for _, p := range jsonlPaths(sid) {
		if fi, err := os.Stat(p); err == nil {
			snap[p] = fi.Size()
		}
	}
	return snap
}

// UserTurnAppendedSince reports whether a `"type":"user"` line landed in sid's log
// AFTER snap was taken. claude persists a submitted prompt as a user line within well
// under a second of a real submit, so this — not tmux send-keys exiting 0, which only
// proves keystrokes reached the pane — is the ground truth that a typed prompt became
// a turn (delivery verification, docs/log/38). Appends are whole lines, so seeking to the
// recorded EOF never splits the type token.
func UserTurnAppendedSince(sid string, snap map[string]int64) bool {
	for _, p := range jsonlPaths(sid) {
		if bytes.Contains(appendedSince(p, snap[p]), []byte(`"type":"user"`)) {
			return true
		}
	}
	return false
}

// PromptAcceptedSince is UserTurnAppendedSince widened by the one other shape a real
// submit takes: typed while the previous turn is still running, claude does not start a
// turn — it QUEUES the prompt (a `queue-operation` line whose content is the prompt) and
// replays it when the turn ends. That is a delivered prompt, but no user line follows for
// minutes, so keying delivery on the user line alone would call it unconfirmed and the
// self-heal would type the whole thing a SECOND time.
//
// The queued half is matched by the prompt's own text rather than the record type,
// because queue-operation also carries claude's internal task-notification enqueues
// (a background agent finishing) — those must not pass for our prompt.
func PromptAcceptedSince(sid string, snap map[string]int64, prompt string) bool {
	needle := jsonNeedle(prompt)
	for _, p := range jsonlPaths(sid) {
		appended := appendedSince(p, snap[p])
		if len(appended) == 0 {
			continue
		}
		if bytes.Contains(appended, []byte(`"type":"user"`)) {
			return true
		}
		if needle != nil && bytes.Contains(appended, needle) {
			return true
		}
	}
	return false
}

// appendedSince returns the bytes written to p after off (capped). Appends are whole
// lines, so seeking to the recorded EOF never splits a token.
func appendedSince(p string, off int64) []byte {
	fi, err := os.Stat(p)
	if err != nil || fi.Size() <= off {
		return nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(f, 4<<20))
	return b
}

// jsonNeedle renders the head of a prompt the way it appears INSIDE a jsonl record, so a
// substring search over raw file bytes works: json.Marshal applies the same escaping
// claude's writer does, and the head is short enough to survive the record wrapping the
// prompt in a preamble. nil when there is nothing usable to match on.
func jsonNeedle(prompt string) []byte {
	head := strings.TrimSpace(strings.SplitN(prompt, "\n", 2)[0])
	if r := []rune(head); len(r) > 24 {
		head = string(r[:24])
	}
	if len([]rune(head)) < 4 {
		return nil // too short to be distinctive — do not match on it
	}
	b, err := json.Marshal(head)
	if err != nil || len(b) < 3 {
		return nil
	}
	return b[1 : len(b)-1] // strip the surrounding quotes
}

// JSONLResumable reports whether sid's log holds an actual conversation (a user or
// assistant turn) — not just bookkeeping lines (Remote Control "bridge-session",
// a lone summary, …). claude --resume fails ("No conversation found") on a stub
// log even though the file exists, so we gate resume on real content.
func JSONLResumable(sid string) bool {
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
