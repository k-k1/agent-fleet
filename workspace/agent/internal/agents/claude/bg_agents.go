package claude

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Background agents (a Task/Agent launched with run_in_background) leave an explicit
// OPEN/CLOSE pair in the session's MAIN transcript — measured on claude 2.1.252:
//
//	open : the Agent tool_result "Async agent launched successfully … agentId: <id>"
//	close: "<task-notification><task-id><id></task-id> … <status>completed</status>"
//
// so "launched, never notified" IS the set of agents still working, with no time window
// in the normal path. That is what this file adds, and it exists because the freshness
// heuristic below (SubagentBusy's original arm) is structurally too coarse on its own: a
// subagent writing a long answer appends NOTHING to its own jsonl while it generates.
// Measured in one session (sf2ykxk, 2026-09-01) the three finished agents had silent
// stretches of 215s / 342s / 396s against a 90s window, and the live one went quiet for
// 4m23s — so the badge fell back to a bare "awaiting input" in the middle of the run,
// exactly when the user needs to know work is still going. Pairing does not flap: the agent's
// close record clears it in the same poll it lands.
//
// Sampling 60 recent transcripts (182 launches) found exactly one open agent — the one
// running at the time — so an unpaired launch is a strong signal, not a common leak. The
// abandon ceiling below covers the case that sample cannot show: claude killed mid-agent,
// so the close record is never written at all.

// bgAgentLaunched is the open marker. It is claude's own tool_result text, matched as a
// substring of the raw record because the id we need sits in that same prose.
const bgAgentLaunched = "Async agent launched successfully"

// bgAgentAbandonTTL bounds how long an unclosed launch keeps counting once its agent has
// gone completely quiet — the safety valve for a close record that will never come. It
// sits an order of magnitude above the write gaps the freshness window must tolerate
// (~400s measured), because ending a healthy agent is the notification's job, not this.
const bgAgentAbandonTTL = 30 * time.Minute

var (
	// bgAgentIDRe pulls the id out of the launch prose; bgAgentTaskIDRe out of the
	// notification envelope. Both ids are the same hex token (verified: the agentId in
	// the launch equals the <task-id> that later reports it, and equals the agent's own
	// subagents/agent-<id>.jsonl).
	bgAgentIDRe     = regexp.MustCompile(`agentId: ([0-9a-f]{8,})`)
	bgAgentTaskIDRe = regexp.MustCompile(`<task-id>([0-9a-f]{8,})</task-id>`)
	// bgAgentTSRe reads the record's own timestamp, so a launch found on the FIRST scan
	// of an existing transcript is aged from when it happened rather than from when the
	// agent process started reading (which would keep a week-old launch "live" for the
	// whole abandon window).
	bgAgentTSRe = regexp.MustCompile(`"timestamp":"([^"]+)"`)
)

// bgAgentScan is one transcript's folded pairing state. Transcripts reach several MB and
// this predicate runs on every session-list poll, so each poll folds in ONLY the bytes
// appended since the last one; off always lands on a record boundary.
type bgAgentScan struct {
	off  int64                // bytes already folded in
	open map[string]time.Time // agentId → its launch time, still unreported
}

var (
	bgAgentMu    sync.Mutex
	bgAgentScans = map[string]*bgAgentScan{}
)

// BackgroundAgentsRunning reports whether sid launched a background agent that has not
// reported back yet.
func BackgroundAgentsRunning(sid string) bool {
	if sid == "" {
		return false
	}
	for _, p := range jsonlPaths(sid) {
		if len(openBackgroundAgents(p)) > 0 {
			return true
		}
	}
	return false
}

// openBackgroundAgents folds p's new bytes into its cached pairing state and returns the
// agents still open.
func openBackgroundAgents(p string) []string {
	bgAgentMu.Lock()
	defer bgAgentMu.Unlock()

	fi, err := os.Stat(p)
	if err != nil {
		delete(bgAgentScans, p) // gone (session deleted / dir removed): drop the state
		return nil
	}
	st := bgAgentScans[p]
	if st == nil {
		st = &bgAgentScan{open: map[string]time.Time{}}
		bgAgentScans[p] = st
	}
	if fi.Size() < st.off {
		// Shorter than what we already read: this is a different conversation under the
		// same path (a fork's --session-id landing here, a rotated log). Re-read it whole
		// rather than folding new records onto a stale open set.
		st.off, st.open = 0, map[string]time.Time{}
	}
	for st.off < fi.Size() {
		b := appendedSince(p, st.off)
		if len(b) == 0 {
			break
		}
		// appendedSince caps its read, which can land mid-record; fold whole lines only
		// and leave the remainder for the next round. A single record larger than that
		// cap has no newline at all — skip past it instead of spinning forever on it (we
		// lose at most that one record, and a launch/close is never that big).
		if cut := bytes.LastIndexByte(b, '\n'); cut >= 0 {
			foldBackgroundAgents(st, b[:cut+1])
			st.off += int64(cut + 1)
		} else {
			st.off += int64(len(b))
		}
	}

	var live []string
	for id, at := range st.open {
		if bgAgentAbandoned(p, id, at) {
			delete(st.open, id)
			continue
		}
		live = append(live, id)
	}
	return live
}

// foldBackgroundAgents folds whole transcript lines into the open set.
//
// Matched on the raw bytes rather than a decoded record because the SAME close event
// arrives in three record shapes (measured): a "user" message carrying the notification
// text (it landed while the main turn was idle), a "queue-operation" (enqueue, then
// remove) when it landed mid-turn and had to be queued, and an "attachment" of type
// queued_command when that queued prompt is finally consumed. Keying on the record type
// would have to enumerate all three and would still miss the next shape claude invents;
// the <task-id> marker is what they all share.
func foldBackgroundAgents(st *bgAgentScan, b []byte) {
	for _, line := range bytes.Split(b, []byte("\n")) {
		if bytes.Contains(line, []byte(bgAgentLaunched)) {
			if m := bgAgentIDRe.FindSubmatch(line); m != nil {
				st.open[string(m[1])] = bgAgentRecordTime(line)
			}
		}
		if bytes.Contains(line, []byte("<task-notification>")) {
			// A notification means the agent STOPPED — whatever its status. It can fire
			// more than once for one id (the user may resume an agent with SendMessage),
			// which is harmless here: closing an id that is already closed is a no-op.
			for _, m := range bgAgentTaskIDRe.FindAllSubmatch(line, -1) {
				delete(st.open, string(m[1]))
			}
		}
	}
}

// bgAgentRecordTime is the record's own timestamp, falling back to now for a line that
// carries none (the abandon clock must start somewhere).
func bgAgentRecordTime(line []byte) time.Time {
	if m := bgAgentTSRe.FindSubmatch(line); m != nil {
		if at, err := time.Parse(time.RFC3339, string(m[1])); err == nil {
			return at
		}
	}
	return time.Now()
}

// bgAgentAbandoned reports whether an unclosed agent has been quiet long enough that we
// stop believing its missing close record. The agent's OWN transcript is the clock —
// mtime, not content, since any append at all proves it is alive — and the launch time
// stands in until that log exists.
func bgAgentAbandoned(main, id string, launched time.Time) bool {
	last := launched
	if fi, err := os.Stat(bgAgentLog(main, id)); err == nil && fi.ModTime().After(last) {
		last = fi.ModTime()
	}
	return time.Since(last) > bgAgentAbandonTTL
}

// bgAgentLog is where claude keeps that agent's own turns. Derived from the MAIN
// transcript path (…/<id>.jsonl → …/<id>/subagents/agent-<agentId>.jsonl, the measured
// layout) so it follows the same sid drift jsonlPaths already resolves.
func bgAgentLog(main, id string) string {
	return filepath.Join(strings.TrimSuffix(main, ".jsonl"), "subagents", "agent-"+id+".jsonl")
}
