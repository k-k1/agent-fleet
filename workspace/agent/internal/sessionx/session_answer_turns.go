package sessionx

import (
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// answerTextMax bounds the text a slim turn keeps per text part, counted after the leading
// whitespace is dropped. The Control Plane keeps only the first non-blank line of an answer, at
// most store.AnswerHeadMax (200) runes, so 1 KiB holds it; the bound also keeps a page of 4000
// turns under the Control Plane's reply bound even when every byte is escaped (6x).
const answerTextMax = 1 << 10

// answerTurn and answerPart are the slim shape of a transcript turn served by
// GET /sessions/{name}/answer-turns: the fields the last-answer read of a memo delivery uses
// (control-plane/internal/memoafter.Turn), and no tool call, edit or output.
type answerPart struct {
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"`
}

type answerTurn struct {
	Role      string       `json:"role"`
	Parts     []answerPart `json:"parts"`
	Text      string       `json:"text,omitempty"`
	Sidechain bool         `json:"sidechain,omitempty"`
	TS        string       `json:"ts"`
	AnchorID  string       `json:"anchorId,omitempty"`
}

// answerTurnsReply is the reply of HandleSessionAnswerTurns; its keys are those of /messages the
// read uses (control-plane memoAnswerReply).
type answerTurnsReply struct {
	Name      string       `json:"name"`
	Messages  []answerTurn `json:"messages"`
	Status    string       `json:"status"`
	FirstLine int          `json:"firstLine"`
	HasMore   bool         `json:"hasMore"`
}

// headOf is what the answer read takes from s: its first line that holds anything, without the
// whitespace around it, cut to at most answerTextMax bytes on a rune boundary. That line is all
// memoafter.ClassifyAnswer uses (it keeps the first 200 runes of it), so the head, the class and
// the "blank or not" of the part are those of the full text. Cutting the text itself would turn a
// "Partial:" that follows a long run of blank lines into a blank part, and the read would take an
// earlier "Done:" for the last answer.
func headOf(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			s = line
			break
		}
	}
	s = strings.TrimSpace(s)
	if len(s) <= answerTextMax {
		return s
	}
	n := answerTextMax
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// slimParts is a turn's parts as the answer read uses them (memoafter.LastAnswer, hasError), in
// at most three parts whatever the turn holds: an error part when the turn has any, the kind of
// the last part that is neither thinking nor a blank text (a response that ended on a tool call or
// a question has no answer), and the run of text parts after it joined into one part, holding its
// head. A turn with nothing but thinking and blank text has no parts: the read passes over it.
func slimParts(parts []transcript.Part) []answerPart {
	var out []answerPart
	for _, p := range parts {
		if p.Kind == "error" {
			out = append(out, answerPart{Kind: "error"})
			break
		}
	}
	var texts []string
	for i := len(parts) - 1; i >= 0; i-- {
		p := parts[i]
		if p.Kind == "thinking" || (p.Kind == "text" && strings.TrimSpace(p.Text) == "") {
			continue
		}
		if p.Kind != "text" {
			if p.Kind != "error" || len(out) == 0 {
				out = append(out, answerPart{Kind: p.Kind})
			}
			break
		}
		texts = append(texts, p.Text)
	}
	if len(texts) > 0 {
		for l, r := 0, len(texts)-1; l < r; l, r = l+1, r-1 {
			texts[l], texts[r] = texts[r], texts[l]
		}
		// The error and the break kind above come first, the text run last, as the parts of the
		// turn read: the run is what follows its last other part.
		out = append(out, answerPart{Kind: "text", Text: headOf(strings.Join(texts, "\n"))})
	}
	return out
}

// slimTurns drops what an answer read never looks at: a turn is its role, its time, its ids and
// slimParts. The size of a turn is bounded (three parts, 1 KiB of text), so a page of n lines is
// bounded by n whatever the transcript holds.
func slimTurns(turns []transcript.Turn) []answerTurn {
	out := make([]answerTurn, 0, len(turns))
	for _, t := range turns {
		s := answerTurn{Role: t.Role, Sidechain: t.Sidechain, TS: t.TS, AnchorID: t.AnchorID, Parts: []answerPart{}}
		if len(t.Parts) == 0 {
			s.Text = headOf(t.Text) // a turn with no parts is read from its text
		} else {
			s.Parts = slimParts(t.Parts)
		}
		out = append(out, s)
	}
	return out
}

// HandleSessionAnswerTurns (GET /sessions/{name}/answer-turns?before=<line>&limit=<n>) serves
// the same window of the transcript as /messages (the tail, or the page that ends at before), with
// every turn slimmed to what the last-answer read of a memo delivery uses. A page of 4000 lines
// holding more than 8 MiB of edit text on /messages is a few hundred KiB here, so the read can
// go back far further in one reply. It carries no aggregates (tasks, files, pending prompts).
//
// firstLine is the first line of the window that was read in full: the claude turns are collected
// without the 1 MiB cap /messages puts on a window, since a cap that dropped the oldest turns of
// the window while firstLine stayed would leave the lines between them unread by the next page.
// A fork that has not written its own log yet answers with no turns (its parent's history, which
// /messages previews, is not this session's answer).
func HandleSessionAnswerTurns(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	meta, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	alive := SessionAlive(meta)
	state := DriveState(meta, alive, true)
	if !AgentOf(meta.Kind).Caps().CanTranscript {
		httpx.WriteErr(w, http.StatusBadRequest, "unsupported_kind", "messages are available for transcript-capable sessions only")
		return
	}
	limit := clampWindowLimit(r.URL.Query().Get("limit"))
	before := 0
	if b, err := strconv.Atoi(r.URL.Query().Get("before")); err == nil && b > 0 {
		before = b
	}
	var turns []transcript.Turn
	var total, lo int
	if transcriptTraitsOf(meta.Kind).ownMessagesPath {
		sid := session.UUID(meta.Dir, name)
		lines, _, _ := claude.TranscriptRead(sid)
		total = len(lines)
		hi := total
		if before > 0 {
			hi = min(before, total)
		}
		lo = max(0, hi-limit)
		turns = claude.CollectTurnsUncapped(lines, lo, hi, claude.RewoundPrompts(lines))
	} else {
		td, _ := AgentOf(meta.Kind).Transcript(meta)
		if td.StepIndexed {
			// before and firstLine are Turn.Idx here, as on /messages: a count would shift
			// when a late turn lands between two pages, repeating or skipping a turn. The
			// window is not capped at 1 MiB as /messages caps it: the slim payload already
			// bounds the text, and a cap would shrink a page the Control Plane has only a
			// few requests to walk back through.
			from, to := stepIndexedPageBounds(td.Turns, before, limit)
			first := 0
			if from > 0 {
				first = td.Turns[from].Idx
			}
			httpx.WriteJSON(w, http.StatusOK, answerTurnsReply{
				Name: name, Messages: slimTurns(td.Turns[from:to]), Status: state, FirstLine: first, HasMore: from > 0,
			})
			return
		}
		total = len(td.Turns)
		hi := total
		if before > 0 {
			hi = min(before, total)
		}
		lo = max(0, hi-limit)
		turns = td.Turns[lo:hi]
	}
	httpx.WriteJSON(w, http.StatusOK, answerTurnsReply{
		Name: name, Messages: slimTurns(turns), Status: state, FirstLine: lo, HasMore: lo > 0,
	})
}
