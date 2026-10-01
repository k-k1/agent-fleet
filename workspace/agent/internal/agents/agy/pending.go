package agy

// Detecting a pending interactive prompt mid-conversation (ASK_QUESTION / tool permission).
//
// Choosing the detection channel (investigated on a real machine 2026-07-20, v1.1.4 —
// docs/log/32): the transcript jsonl writes nothing while a prompt is pending, and there is no
// OSC/title, no stderr and no lock file either. The CLI log (the "Surfacing ask_question" line
// in log/cli-*.log) carries the event but not its body. The only place the structure appears is
// the LAST steps row of the conversation DB (conversations/<uuid>.db): while pending the status
// is 9 (measured: 2 = running, 3 = done, 9 = awaiting user input), and for ask_question the tool
// argument JSON ({"questions":[{question, options, is_multi_select}]}) is embedded in
// step_payload as a plain string (a protobuf length-delimited string, so no schema reversing is
// needed — find where the JSON starts and decode a single value). A pending tool permission is
// the same status=9 (on that tool's step) but carries no question structure, so only the state
// comes back. Scraping the pane (regexes over fixed headings) is fragile against wrapping and
// wording changes, and this DB route is a superset of it, so it is not used.

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (registers "sqlite"), as in opencode

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// Measured steps.status values (v1.1.4 — docs/log/32; 8 and 6 on v1.2.14). A step's status is
// about that step alone: "done" on the last row does NOT mean the turn is over (see turnEnd).
const (
	stepStatusRunning      = 2
	stepStatusDone         = 3
	stepStatusCanceled     = 6
	stepStatusStreaming    = 8
	stepStatusAwaitingUser = 9
)

// lastStepRow is the newest steps row plus where the newest finished turn stopped.
type lastStepRow struct {
	status  int
	payload []byte
	idx     int
	// turnEnd is the step idx the newest executor_metadata row ended at, -1 when no turn has
	// ended yet. hasTurnLog is false when the table is absent (builds before it existed),
	// and only then may the last step's status alone decide idle.
	turnEnd    int
	hasTurnLog bool
}

// lastStep returns the newest step for the slot's conversation. ok=false when the
// conversation isn't adopted yet or the DB is unreadable — callers treat that as "no
// opinion", never as a state.
func lastStep(m session.Meta) (lastStepRow, bool) {
	// The conversation UUID may not be adopted yet when the mirror polls before
	// any sessions-list poll ran (capture normally fires in WireLive) — a first
	// prompt straight into a question would then stay invisible. Capture is
	// idempotent and cheap, so run it here too.
	captureConversation(m)
	conv := sids.Read(session.UUID(m.Dir, m.Name))
	if conv == "" {
		return lastStepRow{}, false
	}
	db, err := sql.Open("sqlite", "file:"+conversationDBPath(conv)+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return lastStepRow{}, false
	}
	defer db.Close()
	r := lastStepRow{turnEnd: -1}
	if err := db.QueryRow(`SELECT idx, status, step_payload FROM steps ORDER BY idx DESC LIMIT 1`).
		Scan(&r.idx, &r.status, &r.payload); err != nil {
		return lastStepRow{}, false
	}
	r.turnEnd, r.hasTurnLog = turnEnd(db)
	return r, true
}

// turnEnd reads where the newest finished turn stopped. agy writes one executor_metadata row
// each time a turn's executor run ends — completed or canceled by Esc — and its top-level
// field 3 is the idx of the last step of that run (measured on v1.2.14, every conversation on
// the machine: the newest row's field 3 equals the final step's idx, and field 1 is 4 for a
// completed run, 2 for a canceled one). Nothing else marks the end of a turn: the model's step
// row only appears once its response is complete, so mid-turn the last row is regularly a
// finished user, tool or background-task-notification step, and a text-only reply can still be
// followed by more work when a background command finishes.
func turnEnd(db *sql.DB) (int, bool) {
	var data []byte
	switch err := db.QueryRow(`SELECT data FROM executor_metadata ORDER BY idx DESC LIMIT 1`).Scan(&data); {
	case err == sql.ErrNoRows:
		return -1, true
	case err != nil:
		return -1, false
	}
	if v, ok := protoVarintField(data, 3); ok {
		return int(v), true
	}
	return -1, true
}

// protoVarintField returns the first top-level varint field num of a protobuf message,
// walking the wire format just far enough to skip the other fields.
func protoVarintField(b []byte, num uint64) (uint64, bool) {
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			return 0, false
		}
		b = b[n:]
		switch key & 7 {
		case 0:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return 0, false
			}
			if key>>3 == num {
				return v, true
			}
			b = b[n:]
		case 1:
			if len(b) < 8 {
				return 0, false
			}
			b = b[8:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || uint64(len(b)-n) < l {
				return 0, false
			}
			b = b[n+int(l):]
		case 5:
			if len(b) < 4 {
				return 0, false
			}
			b = b[4:]
		default:
			return 0, false
		}
	}
	return 0, false
}

// LiveState is agy's session state derived from the conversation DB, mirroring
// opencode.LiveState: "question"/"permission" while blocked on the user,
// "working" mid-turn, "idle" once the turn has ended (turnEnd), "" when the DB has
// no opinion yet. agy ships no status hooks, so this is the ONLY turn-end
// signal — /input persists an optimistic "working" that nothing else clears,
// which left the operator's completion-report arm unconsumed forever (docs/log/30 item 2).
// Callers gate on liveness themselves: a killed session's DB keeps its last
// status, which must not surface as live state on a stopped session.
func LiveState(m session.Meta) string {
	r, ok := lastStep(m)
	if !ok {
		return ""
	}
	switch r.status {
	case stepStatusAwaitingUser:
		if qs := parseAskQuestions(r.payload); len(qs) > 0 {
			return "question"
		}
		return "permission"
	case stepStatusRunning, stepStatusStreaming:
		return "working"
	}
	if r.hasTurnLog {
		// Reading a finished last step as idle put a session that was thinking after a tool
		// call (or after the prompt itself) into waiting-for-input, and fired a completion
		// report mid-turn.
		if r.turnEnd >= r.idx {
			return "idle"
		}
		return "working"
	}
	switch r.status {
	case stepStatusDone, stepStatusCanceled:
		return "idle"
	}
	return ""
}

func conversationDBPath(conv string) string {
	return filepath.Join(stateDir(), "conversations", conv+".db")
}

// Probe reports whether the slot's conversation is blocked on an interactive
// prompt right now: ("question", parsed questions) for ASK_QUESTION,
// ("permission", synthesized menu) for a tool-permission prompt, ("", nil)
// otherwise. Callers gate on liveness themselves — a killed session's DB may
// keep a stale status=9 last step, which must not surface as pending on a
// stopped session.
func Probe(m session.Meta) (string, []transcript.Question) {
	r, ok := lastStep(m)
	if !ok || r.status != stepStatusAwaitingUser {
		return "", nil
	}
	if qs := parseAskQuestions(r.payload); len(qs) > 0 {
		return "question", qs
	}
	return "permission", permissionQuestions(r.payload)
}

// PendingModal hands the wait-for-a-human state, as it stood just before the pane was folded
// up, over to the carry-forward (docs/log/75 P5). It is a thin mapping over Probe and differs
// only in how Kind is resolved: a permission becomes "permission".
//
// Probe answers a permission with a synthesized menu (Yes / No …) so that a key sequence can be
// fired at a LIVE TUI. Drawing that menu after the fold-up would have the user pick an answer
// with nothing left to send it to (docs/log/75 §75.6.4). All that can be carried is which
// command was being asked about, and the synthesized question text already holds it.
//
// agy's pending state survives in the conversation DB, so it can be read after the pane has
// died — unlike the three ACP kinds, it can still be picked up at an occasion later than halt
// (when the session list notices the stop).
func (agentImpl) PendingModal(m session.Meta) (agents.PendingModal, bool) {
	st, qs := Probe(m)
	switch st {
	case "question":
		if len(qs) == 0 {
			return agents.PendingModal{}, false
		}
		return agents.PendingModal{Kind: "question", Questions: qs}, true
	case "permission":
		detail := ""
		if len(qs) > 0 {
			detail = qs[0].Question
		}
		return agents.PendingModal{Kind: "permission", Detail: detail}, true
	}
	return agents.PendingModal{}, false
}

// permissionQuestions synthesizes the pending permission menu as a Question so
// the Console's menu-mode card (Down×i + Enter) can drive it — labels and row
// COUNT must mirror the TUI's menu exactly or the keys land on the wrong row
// (measured on v1.1.4: run_command has 4 rows, write_to_file / replace_file_content
// have 2). The pending tool's name rides in the payload as a plain string, so an
// unknown tool (different menu shape we haven't verified) yields NO card —
// state=permission alone, answer in the terminal — rather than a mis-keying one.
func permissionQuestions(payload []byte) []transcript.Question {
	args := parseArgsJSON(payload)
	switch {
	case bytes.Contains(payload, []byte("run_command")):
		return []transcript.Question{{
			Question: "Requesting permission for: " + args["CommandLine"],
			Options: []transcript.Option{
				{Label: "Yes"},
				{Label: "Yes — always in this conversation"},
				{Label: "Yes — always (persist to settings.json)"},
				{Label: "No"},
			},
		}}
	case bytes.Contains(payload, []byte("write_to_file")):
		return []transcript.Question{{
			Question: "Allow creation of this file? " + args["TargetFile"],
			Options:  []transcript.Option{{Label: "Yes, allow creation"}, {Label: "No, deny creation"}},
		}}
	case bytes.Contains(payload, []byte("replace_file_content")):
		return []transcript.Question{{
			Question: "Accept this file edit? " + args["TargetFile"],
			Options:  []transcript.Option{{Label: "Yes, accept this change"}, {Label: "No, reject this change"}},
		}}
	}
	return nil
}

// parseArgsJSON pulls the pending tool's argument JSON (the first decodable
// {"…"} object embedded in the payload) into a flat string map, best-effort —
// a miss just leaves the synthesized question without its detail suffix.
func parseArgsJSON(payload []byte) map[string]string {
	out := map[string]string{}
	for i := 0; i+2 < len(payload); {
		j := bytes.Index(payload[i:], []byte(`{"`))
		if j < 0 {
			break
		}
		i += j
		var doc map[string]any
		if json.NewDecoder(bytes.NewReader(payload[i:])).Decode(&doc) == nil && len(doc) > 0 {
			for k, v := range doc {
				if s, ok := v.(string); ok {
					out[k] = s
				}
			}
			return out
		}
		i += 2
	}
	return out
}

// parseAskQuestions extracts the ask_question tool-args JSON embedded in a
// step payload. The surrounding bytes are protobuf wire garbage; json.Decoder
// stops at the end of the first value, so locating `{"questions":` is enough.
// The TUI appends its own trailing "Write-in..." row that is NOT in this list,
// so option indices here align 1:1 with the widget's numbered rows — the
// Console's menu-mode key driving (Down×i, Enter) lands on the right option.
func parseAskQuestions(payload []byte) []transcript.Question {
	i := bytes.Index(payload, []byte(`{"questions":`))
	if i < 0 {
		return nil
	}
	var doc struct {
		Questions []struct {
			Question      string   `json:"question"`
			Options       []string `json:"options"`
			IsMultiSelect bool     `json:"is_multi_select"`
		} `json:"questions"`
	}
	if json.NewDecoder(bytes.NewReader(payload[i:])).Decode(&doc) != nil || len(doc.Questions) == 0 {
		return nil
	}
	out := make([]transcript.Question, 0, len(doc.Questions))
	for _, q := range doc.Questions {
		tq := transcript.Question{Question: q.Question, MultiSelect: q.IsMultiSelect}
		for _, o := range q.Options {
			tq.Options = append(tq.Options, transcript.Option{Label: o})
		}
		out = append(out, tq)
	}
	return out
}
