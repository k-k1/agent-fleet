package agy

// The transcript reader for the chat mirror (generic /messages). agy appends to
// brain/<conversation-uuid>/.system_generated/logs/transcript_full.jsonl per conversation,
// LIVE while a turn is still running (verified on real hardware: the brain directory and
// the jsonl appear right after the first prompt, and the PLANNER_RESPONSE line is readable
// by the time the answer completes). The conversation DB supplies missing turn
// boundaries and executor failure details. One JSONL line records one step:
//
//	{"step_index":N,"source":"USER_EXPLICIT|MODEL|SYSTEM","type":"USER_INPUT|
//	 PLANNER_RESPONSE|<TOOL_NAME>|…","status":"DONE","created_at":…,"content":…}
//
// The mapping: USER_INPUT → a user turn (only the inside of <USER_REQUEST> is extracted);
// MODEL/PLANNER_RESPONSE yields assistant text. Other MODEL rows become tool
// parts, named by their type or a recognizable GENERIC result. An ERROR_MESSAGE
// becomes an error part when matched to the DB. Other errors remain tool parts.
// System bookkeeping such as checkpoints is model-facing.

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// brainDir is agy's per-conversation artifact root (plans, logs, uploads).
func brainDir() string { return filepath.Join(stateDir(), "brain") }

// transcriptPath prefers transcript_full.jsonl (never truncated) over
// transcript.jsonl (the model-facing view, which drops steps once agy
// checkpoints a long conversation).
func transcriptPath(conv string) string {
	logs := filepath.Join(brainDir(), conv, ".system_generated", "logs")
	full := filepath.Join(logs, "transcript_full.jsonl")
	if _, err := os.Stat(full); err == nil {
		return full
	}
	return filepath.Join(logs, "transcript.jsonl")
}

func (agentImpl) Transcript(m session.Meta) (agents.TranscriptData, bool) {
	// The conversation UUID is adopted by captureConversation (brain-dir diff
	// while alive, cwd-map on graceful exit). Before the first prompt lands
	// there is no conversation yet — an empty transcript is the truth.
	conv := sids.Read(session.UUID(m.Dir, m.Name))
	td := agents.TranscriptData{StepIndexed: true}
	if conv == "" {
		return td, true
	}
	path := transcriptPath(conv)
	td.Path = path
	// A pending ASK_QUESTION / permission prompt never reaches the jsonl (measured:
	// nothing is recorded while it is pending), so the interactive card's payload
	// comes from the conversation-DB probe: real options for questions, a synthesized menu for
	// permissions (pending.go). The generic /messages handler only surfaces
	// Pending while the session is alive, which gates out a killed session's
	// stale status=9 row.
	if _, qs := Probe(m); len(qs) > 0 {
		td.Pending = qs
	}
	var log io.Reader = strings.NewReader("")
	if f, err := os.Open(path); err == nil {
		defer f.Close()
		log = f
	}
	users, failures := transcriptDatabaseSteps(conv)
	if failure, ok := hookExecutorFailure(m, conv); ok &&
		(len(failures) == 0 || failures[len(failures)-1].index < failure.index) {
		failures = append(failures, failure)
	}
	td.Turns = parseTranscript(log, users, failures)
	// A conversation that never switched models records none, so its turns carry the model
	// the session was launched (or last resumed) with.
	stampModel(td.Turns, modelLabel(m.Model))
	return td, true
}

// stampModel labels every still-unlabelled assistant turn with model, for the mirror's
// per-response badge.
func stampModel(turns []transcript.Turn, model string) {
	if model == "" {
		return
	}
	for i := range turns {
		if turns[i].Role == "assistant" && turns[i].Model == "" {
			turns[i].Model = model
		}
	}
}

// modelLabel is the inverse of modelID: the display name `agy models` lists for id, or id
// itself when the catalog has not been fetched. The transcript's switch notes carry display
// names, so both sources then read the same. The catalog is walked in its listed order so an
// id listed under two names always answers with the same one.
func modelLabel(id string) string {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	for _, c := range modelsList {
		if c.ID == id {
			return c.Label
		}
	}
	return id
}

// stepLine is one transcript_full.jsonl row (fields we read).
type stepLine struct {
	Index   *int   `json:"step_index"`
	Source  string `json:"source"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

// userRequestRe extracts the actual prompt from agy's USER_INPUT wrapper — the
// content also carries <ADDITIONAL_METADATA> (local time) and
// <USER_SETTINGS_CHANGE> blocks meant for the model, not the user.
var userRequestRe = regexp.MustCompile(`(?s)<USER_REQUEST>\n?(.*?)\n?</USER_REQUEST>`)

// Both the tool result and its serializer can add timestamps. Strip the whole
// prefix while keeping matching lines inside the result body.
var stepMetaLineRe = regexp.MustCompile(`^(?:(?:Created At|Completed At): [^\n]*\n?)+`)

// toolOutputMax bounds a tool part's output shown in the mirror (the terminal
// remains the raw view; the mirror wants the gist).
const toolOutputMax = 4000

// commandIndentRe is the 4-tab indent agy's RUN_COMMAND result template puts on
// every line after the status sentence ("The command completed successfully.\n
// \t\t\t\tOutput:\n\t\t\t\t<line>…"), which renders as a ragged block in the mirror's
// tool part. Exactly the template's prefix is stripped per line — output that is
// itself tab-indented keeps its own tabs (they sit after the template's four).
var commandIndentRe = regexp.MustCompile(`(?m)^\t{4}`)

func stripCommandIndent(s string) string { return commandIndentRe.ReplaceAllString(s, "") }

type mirrorStep struct {
	index    int
	user     bool
	from, to string
	part     transcript.Part
}

func readMirrorSteps(log io.Reader, failures []executorErrorStep) []mirrorStep {
	knownErrors := make(map[int]bool, len(failures))
	for _, failure := range failures {
		knownErrors[failure.index] = true
	}
	var steps []mirrorStep
	sc := bufio.NewScanner(log)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // steps can carry large tool output
	nextIndex := 0
	for sc.Scan() {
		var s stepLine
		if json.Unmarshal(sc.Bytes(), &s) != nil {
			continue
		}
		step := mirrorStep{index: nextIndex}
		if s.Index != nil {
			step.index = *s.Index
		}
		nextIndex = max(nextIndex, step.index+1)
		switch {
		case s.Type == "USER_INPUT":
			step.user = true
			step.from, step.to, _ = modelSwitch(s.Content)
			text := s.Content
			if m := userRequestRe.FindStringSubmatch(text); m != nil {
				text = m[1]
			}
			step.part = transcript.Part{Kind: "text", Text: strings.TrimSpace(text)}
		case s.Source == "MODEL" && s.Type == "PLANNER_RESPONSE":
			text := strings.TrimSpace(s.Content)
			if text == "" {
				continue
			}
			step.part = transcript.Part{Kind: "text", Text: text}
		case s.Type == "ERROR_MESSAGE" && s.Index != nil && knownErrors[step.index]:
			continue // The DB supplies the sanitized error at this step, even if this line arrived late.
		case s.Source == "MODEL", s.Type == "ERROR_MESSAGE":
			step.part = toolStep(s.Type, s.Content)
		default:
			continue // System bookkeeping is model-facing context management.
		}
		steps = append(steps, step)
	}
	// A late line can be written after later turns. Keep only normalized parts in
	// memory so sorting never retains the full, potentially large tool payloads.
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].index < steps[j].index })
	return steps
}

func parseTranscript(log io.Reader, users []int, failures []executorErrorStep) []transcript.Turn {
	var turns []transcript.Turn
	var cur *transcript.Turn // open assistant turn
	flush := func() {
		if cur != nil {
			var texts []string
			for _, p := range cur.Parts {
				if p.Kind == "text" && p.Text != "" {
					texts = append(texts, p.Text)
				}
			}
			cur.Text = strings.Join(texts, "\n\n")
			turns = append(turns, *cur)
			cur = nil
		}
	}
	userIndex := -1
	model := "" // the model named by the latest switch note
	startTurn := func(index int) {
		if userIndex != index {
			flush()
			userIndex = index
		}
	}
	addPart := func(index int, part transcript.Part) {
		if cur == nil {
			// Reserve the odd index after the prompt for its response, including
			// when the first available part comes from the DB or a checkpoint.
			anchor := index
			if userIndex >= 0 {
				anchor = userIndex
			}
			cur = &transcript.Turn{Role: "assistant", Idx: 2*anchor + 1, Model: model}
		}
		cur.Parts = append(cur.Parts, part)
	}
	addFailure := func() {
		failure := failures[0]
		// The Console may see this failure after the user sends a newer prompt.
		addPart(failure.index, transcript.Part{Kind: "error", Text: failure.text, Cause: "agy_executor_failure"})
		failures = failures[1:]
	}
	advance := func(before int) {
		for len(users) > 0 && users[0] <= before {
			for len(failures) > 0 && failures[0].index < users[0] {
				addFailure()
			}
			startTurn(users[0])
			users = users[1:]
		}
		for len(failures) > 0 && failures[0].index < before {
			addFailure()
		}
	}
	steps := readMirrorSteps(log, failures)
	for i, step := range steps {
		if i+1 < len(steps) && steps[i+1].index == step.index {
			continue // The last write for this step supersedes earlier copies.
		}
		advance(step.index)
		if step.user {
			startTurn(step.index)
			// agy writes no model on its own steps; the only record is the switch note it
			// prefixes to the next prompt. The note's "from" is what answered every turn
			// before it, so the first note also labels the turns already parsed.
			if step.to != "" {
				if model == "" && step.from != "None" {
					stampModel(turns, step.from)
				}
				model = step.to
			}
			if step.part.Text != "" {
				turns = append(turns, transcript.Turn{
					Role: "user", Text: step.part.Text, Idx: 2 * step.index,
					Parts: []transcript.Part{step.part},
				})
			}
		} else {
			addPart(step.index, step.part)
		}
	}
	for len(users) > 0 {
		advance(users[0])
	}
	for len(failures) > 0 {
		addFailure()
	}
	flush()
	return turns
}
