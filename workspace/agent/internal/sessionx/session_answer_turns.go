package sessionx

import (
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// answerTextMax bounds the text a slim turn keeps per part. The Control Plane keeps only the
// first line of an answer (a few hundred runes), so the head is all it needs; a tool's output or
// an edit's body never is.
const answerTextMax = 4 << 10

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

// headOf cuts s to at most answerTextMax bytes on a rune boundary.
func headOf(s string) string {
	if len(s) <= answerTextMax {
		return s
	}
	n := answerTextMax
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// slimTurns drops what an answer read never looks at. Only text, thinking and error parts keep a
// body (cut to a head); every other part keeps its kind alone, since the read tells a response
// that ended on a tool call from one that ended on text by the kind of the last part.
func slimTurns(turns []transcript.Turn) []answerTurn {
	out := make([]answerTurn, 0, len(turns))
	for _, t := range turns {
		s := answerTurn{Role: t.Role, Sidechain: t.Sidechain, TS: t.TS, AnchorID: t.AnchorID, Parts: []answerPart{}}
		if len(t.Parts) == 0 {
			s.Text = headOf(t.Text) // a turn with no parts is read from its text
		}
		for _, p := range t.Parts {
			sp := answerPart{Kind: p.Kind}
			if p.Kind == "text" || p.Kind == "thinking" || p.Kind == "error" {
				sp.Text = headOf(p.Text)
			}
			s.Parts = append(s.Parts, sp)
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
		turns = claude.CollectTurnsHiding(lines, lo, hi, claude.RewoundPrompts(lines))
	} else {
		td, _ := AgentOf(meta.Kind).Transcript(meta)
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
