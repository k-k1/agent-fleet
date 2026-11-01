package opencode

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	_ "modernc.org/sqlite" // pure-Go SQLite driver (registers "sqlite"), as in the CP
)

// opencode chat transcript. The conversation is read through af's serve (v2state.go) or, for a
// session whose server af cannot reach, from opencode's SQLite store (v2store.go), and normalized
// into the SAME transcript.Turn model the Console chat consumes for claude/codex. This file holds
// the entry points and the message/part rendering both readers share: opencode 2's messages are
// rendered through the opencode 1 message/part shape (v2state.go builds it), so parseMessage below
// is the one renderer.

// dbPath is the shared SQLite store the opencode TUI sessions write.
func dbPath() string {
	return filepath.Join(paths.HomeDir(), ".local", "share", "opencode", "opencode.db")
}

// openRODB opens the store at p read-only (WAL auto-detected; busy_timeout so a concurrent
// opencode write can't wedge us). ok=false when the db is absent/unopenable.
func openRODB(p string) (*sql.DB, bool) {
	if _, err := os.Stat(p); err != nil {
		return nil, false
	}
	db, err := sql.Open("sqlite", "file:"+p+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return nil, false
	}
	return db, true
}

// metaCreatedMs is the slot's creation time as epoch millis (opencode's time unit).
// A missing/unparsable CreatedAt yields 0 — the permissive pre-fix behavior — so a
// legacy meta without the field keeps its conversation rather than losing it.
func metaCreatedMs(m session.Meta) int64 {
	if m.CreatedAt == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, m.CreatedAt)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// LiveState derives opencode's working/idle/question state from the conversation's reader
// (v2state.go): working while af's serve lists the session as running, question while it waits on
// a form, idle otherwise. Returns "" when the state can't be derived, and the caller falls back to
// the plugin status store: the daemon can't be asked, or the session runs on a private
// --standalone server af cannot reach.
//
// Why "" and not "idle" on a failed read: "idle" asserts the *strongest* claim — the turn is done
// — from the *least* information, and it fails silently: a live turn would read as waiting for
// input with no stop button, which is exactly the claude false-idle bug (there, a TUI footer string
// moved). "" degrades to the plugin status instead of guessing.
func LiveState(m session.Meta) string { return liveStateV2(v2TargetFor(m), m) }

// readTranscript reads the slot's current opencode conversation as normalized chat turns. ok is
// always true; no conversation yet yields nil turns (an empty chat).
func readTranscript(m session.Meta) (agents.TranscriptData, bool) {
	return readTranscriptV2(v2TargetFor(m), m), true
}

// isCompacting reports whether the session is compacting its conversation right now — the
// session list's "compacting" state (WireLive).
func isCompacting(m session.Meta) bool { return compactingV2(v2TargetFor(m), m) }

// compactionProbe is the part of an opencode 1 message.updated event's info that says whether
// it is a compaction summary and whether it has finished (driver.go).
type compactionProbe struct {
	Role  string `json:"role"`
	Mode  string `json:"mode"`
	Agent string `json:"agent"`
	Time  struct {
		Completed int64 `json:"completed"`
	} `json:"time"`
}

func (p compactionProbe) isCompaction() bool {
	return p.Role == "assistant" && (p.Mode == "compaction" || p.Agent == "compaction")
}

func (p compactionProbe) inFlight() bool { return p.isCompaction() && p.Time.Completed == 0 }

// TerminalModal is the modal an opencode Terminal pane shows that typed text would decide:
// "question" while the question tool waits, "permission" while the permission prompt is up
// (screen.go), "" otherwise. A pasted line is dropped in both and the Enter picks the
// highlighted option (measured 1.18.33). A managed session answers "": its driver refuses free
// text itself (ErrQuestionPending). So does a pane that is not running.
func TerminalModal(m session.Meta) string {
	if m.DriverKind() == session.DriverManaged || !tmuxx.HasSession(session.TmuxName(m.Name)) {
		return ""
	}
	if LiveState(m) == "question" {
		return "question"
	}
	if terminalPermission(m) {
		return "permission"
	}
	return ""
}

// questions parses a question tool's state.input into transcript.Questions (identical
// schema to claude's AskUserQuestion: questions[]{question,header,multiSelect,options}).
func questions(input json.RawMessage) []transcript.Question {
	if len(input) == 0 {
		return nil
	}
	var in struct {
		Questions []transcript.Question `json:"questions"`
	}
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	return in.Questions
}

// answer pulls the chosen answer text out of a completed question tool's output
// ("User has answered your questions: \"Q\"=\"A\". …") for the answered block's display.
func answer(output string) string {
	// Take the text inside the first ="…" pair (the selected label).
	i := strings.Index(output, `="`)
	if i < 0 {
		return ""
	}
	rest := output[i+2:]
	if j := strings.IndexByte(rest, '"'); j >= 0 {
		return rest[:j]
	}
	return ""
}

// usageTurns is the conversation as the usage fold reads it: readTranscript's turns plus the
// CostOnly ones.
func usageTurns(m session.Meta) []transcript.Turn { return usageTurnsV2(v2TargetFor(m), m) }

// parseMessage builds a transcript.Turn from one message and its parts in the opencode 1
// message/part shape (v2SessionTurns builds it from an opencode 2 message). idx is the message ordinal (a stable render key
// + the unit the generic windower pages over). win answers the model's context window and
// may be nil (no window recorded, and the Console guesses from the model name as before).
func parseMessage(msgID string, data []byte, partRows [][]byte, idx int, win windowFn) (transcript.Turn, bool) {
	var md struct {
		Role       string `json:"role"`
		ModelID    string `json:"modelID"`
		ProviderID string `json:"providerID"` // with ModelID, the key the declared window is filed under
		Variant    string `json:"variant"`    // opencode's reasoning effort/variant (e.g. "max")
		// Cost is this message's own USD cost, summed over its steps (measured 1.18.34: per
		// message, not cumulative — session.cost is the running total — filled at each
		// step-finish and final at time.completed; 0 on a free model).
		Cost   float64 `json:"cost"`
		Tokens struct {
			Input  int `json:"input"`
			Output int `json:"output"`
			Cache  struct {
				Read  int `json:"read"`
				Write int `json:"write"`
			} `json:"cache"`
		} `json:"tokens"`
		Time struct {
			Created   int64 `json:"created"`
			Completed int64 `json:"completed"` // assistant only; absent while the turn runs
		} `json:"time"`
		Path struct {
			Cwd string `json:"cwd"`
		} `json:"path"`
	}
	if json.Unmarshal(data, &md) != nil {
		return transcript.Turn{}, false
	}
	if md.Role != "user" && md.Role != "assistant" {
		return transcript.Turn{}, false // system / synthetic messages are not part of the chat
	}
	parts, text := readParts(partRows)
	// A failed turn (errors.go) carries its reason in the message row, not in a part,
	// and typically has NO parts at all — so the error part has to be appended BEFORE
	// the empty-turn drop below, or the turn (and the only explanation the user gets)
	// vanishes from the mirror while the session quietly reads idle.
	if e, ok := decodeMessageError(data); ok {
		parts = append(parts, e.part())
		// Text is the flattened form (copy button, get_session_output, chat bridge):
		// the operator must see the failure there too, not just in the rendered block.
		if text != "" {
			text += "\n\n"
		}
		text += e.summary()
	}
	if len(parts) == 0 {
		return transcript.Turn{}, false
	}
	t := transcript.Turn{
		Role: md.Role, Parts: parts, Text: text, Idx: idx,
		Cwd: md.Path.Cwd,
		// opencode's own message id IS the fork anchor: POST /session/{id}/fork takes a
		// messageID and its copy loop stops at the first message whose id sorts >= that
		// value (docs/log/55 §55.2), so the anchor travels to the server untranslated.
		AnchorID: msgID,
	}
	if md.Time.Created > 0 {
		// opencode stores epoch milliseconds.
		t.TS = time.UnixMilli(md.Time.Created).UTC().Format(time.RFC3339)
	}
	if md.Role == "assistant" {
		// One opencode message IS one whole turn (text + every tool part), so created is
		// the turn's START. completed lands when the turn ends — that is what the mirror's
		// footer wants. Still running (completed == 0): leave EndTS empty and let the
		// Console fall back to created.
		if md.Time.Completed > 0 {
			t.EndTS = time.UnixMilli(md.Time.Completed).UTC().Format(time.RFC3339)
		}
		t.Model = md.ModelID
		t.Effort = md.Variant
		t.InTok, t.OutTok = md.Tokens.Input, md.Tokens.Output
		t.CacheRead, t.CacheCreate = md.Tokens.Cache.Read, md.Tokens.Cache.Write
		t.CostUSD = md.Cost
		// The declared window, so the gauge is measured against what opencode itself is
		// using rather than against usagex.WindowGuess's 200,000 (window.go: a self-hosted
		// 32k engine read as 13% full while it compacted on every turn).
		if win != nil {
			t.CtxWindow = win(md.ProviderID, md.ModelID)
		}
	}
	return t, true
}

// readParts maps a message's part rows, in order, onto transcript.Parts: text → rendered
// Markdown, tool/patch → a faint trace, reasoning/step framing → dropped. Returns the
// parts and the concatenated text (for copy).
func readParts(partRows [][]byte) ([]transcript.Part, string) {
	var parts []transcript.Part
	var sb strings.Builder
	for _, pd := range partRows {
		var p struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Tool  string `json:"tool"`
			State struct {
				Status string          `json:"status"`
				Input  json.RawMessage `json:"input"`
				Output string          `json:"output"`
			} `json:"state"`
			Files []string `json:"files"`
		}
		if json.Unmarshal(pd, &p) != nil {
			continue
		}
		switch p.Type {
		case "text":
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			parts = append(parts, transcript.Part{Kind: "text", Text: p.Text})
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(p.Text)
		case "reasoning":
			// opencode's chain-of-thought — a collapsible thinking block (like claude's).
			if strings.TrimSpace(p.Text) != "" {
				parts = append(parts, transcript.Part{Kind: "thinking", Text: p.Text})
			}
		case "tool":
			name := p.Tool
			if name == "" {
				name = "tool"
			}
			// opencode's `question` tool is claude's AskUserQuestion: a completed one shows
			// as an answered question block; a running one is surfaced as the pending
			// question (pending), so it's skipped here.
			if name == "question" {
				if p.State.Status == "completed" {
					if qs := questions(p.State.Input); len(qs) > 0 {
						parts = append(parts, transcript.Part{Kind: "question", Tool: "question", Questions: qs, Answer: answer(p.State.Output)})
					}
				}
				continue
			}
			if name == "task" {
				var in struct {
					Description  string `json:"description"`
					Prompt       string `json:"prompt"`
					SubagentType string `json:"subagent_type"`
					Model        string `json:"model"`
				}
				if json.Unmarshal(p.State.Input, &in) == nil {
					status := strings.ToLower(strings.TrimSpace(p.State.Status))
					switch status {
					case "pending", "queued":
						status = "requested"
					case "in_progress":
						status = "running"
					case "error", "cancelled":
						status = "failed"
					case "completed", "running":
					default:
						status = "requested"
					}
					parts = append(parts, transcript.Part{
						Kind: "delegation", Tool: name, Info: strings.TrimSpace(in.Description),
						Prompt: strings.TrimSpace(in.Prompt), AgentType: in.SubagentType,
						Model: in.Model, Status: status, Output: transcript.CapOutput(p.State.Output),
					})
					continue
				}
			}
			part := transcript.Part{Kind: "tool", Tool: name, Info: toolInfo(p.State.Input), Output: transcript.CapOutput(p.State.Output)}
			// Edit-family tools carry before/after so the trace opens as a diff pane.
			if f, es := toolEdits(name, p.State.Input); len(es) > 0 {
				part.File, part.Edits = f, es
			}
			parts = append(parts, part)
			// af's generate_image (ADR 0069) becomes a picture card right after its trace.
			// No pairing is needed here, unlike claude: opencode keeps a tool's output on the
			// same part, so the result is in hand the moment the call completes.
			if img, ok := transcript.GeneratedImagePart(name, p.State.Output); ok {
				parts = append(parts, img)
			}
		case "patch":
			// A committed edit; opencode records only the file list + hash, so it's a
			// trace (no before/after to open as a diff).
			parts = append(parts, transcript.Part{Kind: "tool", Tool: "patch", Info: transcript.Clip(strings.Join(p.Files, ", "))})
		}
		// step-start / step-finish are framing — dropped.
	}
	return parts, strings.TrimSpace(sb.String())
}

// toolInfo renders a short one-line summary of a tool call's input (command /
// path / pattern), reusing transcript.Clip for trimming.
func toolInfo(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(input, &m) != nil {
		return ""
	}
	// description last: it's the task (subagent) tool's summary, but a more specific
	// field (command/path/pattern) should win when both exist.
	for _, k := range []string{"command", "file_path", "filePath", "path", "pattern", "query", "url", "description"} {
		if v, ok := m[k].(string); ok && v != "" {
			return transcript.Clip(v)
		}
	}
	return ""
}

// toolEdits extracts before/after content for opencode's edit-family tools so
// the Console can open a diff pane: `write` is all-added (Old=""), `edit` carries the
// old/new strings. Other tools return nil (they stay a plain trace).
func toolEdits(name string, input json.RawMessage) (string, []transcript.Edit) {
	if len(input) == 0 {
		return "", nil
	}
	switch name {
	case "write":
		var in struct {
			FilePath string `json:"filePath"`
			Content  string `json:"content"`
		}
		if json.Unmarshal(input, &in) != nil || in.FilePath == "" {
			return "", nil
		}
		return in.FilePath, []transcript.Edit{{Old: "", New: transcript.CapEdit(in.Content)}}
	case "edit":
		var in struct {
			FilePath  string `json:"filePath"`
			OldString string `json:"oldString"`
			NewString string `json:"newString"`
		}
		if json.Unmarshal(input, &in) != nil || in.FilePath == "" {
			return "", nil
		}
		return in.FilePath, []transcript.Edit{{Old: transcript.CapEdit(in.OldString), New: transcript.CapEdit(in.NewString)}}
	}
	return "", nil
}
