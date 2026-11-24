package memoafter

import (
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The last answer of a delivery's root session (issue #4876): what the run said it did, read from
// the Agent's transcript after the delivery closed.

// partialHeads are the first words of a run that stopped part way, as the unattended implementer
// is told to begin its last answer. Matched like Done:, after the same clean-up.
var partialHeads = []struct {
	word  string
	colon bool
}{{"partial", true}, {"held", true}, {"deferred", true}, {"too large", true}, {"stopped at part", false}}

// TurnPart and Turn are the part of the Agent's transcript turns (GET /sessions/{name}/messages)
// the answer is read from.
type TurnPart struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type Turn struct {
	Role      string     `json:"role"`
	Parts     []TurnPart `json:"parts"`
	Text      string     `json:"text"`
	Sidechain bool       `json:"sidechain"`
	// TS is when the turn was written (RFC 3339, the Agent host's clock), "" when the kind keeps none.
	TS string `json:"ts"`
}

// TurnsBefore is turns up to the first user turn of the session itself (not a subagent's) stamped
// at or after pin, the first instant past the run's end: a person's input that came later, and
// everything after it, is not the run's. Only a user turn cuts: an assistant turn may be stamped
// when it is read rather than when it was written (measured: codex's synthetic error turn of a
// refused turn/start is stamped time.Now()), and dropping the run's own last error would read an
// earlier Done:. A turn whose time is missing or does not parse is kept, as nothing tells it
// apart: such a kind reads as it did without the pin.
func TurnsBefore(turns []Turn, pin time.Time) []Turn {
	for i, t := range turns {
		if t.Role != "user" || t.Sidechain {
			continue
		}
		if at, err := time.Parse(time.RFC3339Nano, t.TS); err == nil && !at.Before(pin) {
			return turns[:i]
		}
	}
	return turns
}

// PinnedAnswer is LastAnswer of the turns before pin (TurnsBefore), turns being the transcript
// from some line to its end. Every line of the run was written before pin, so an assistant turn
// stamped at or after it is a later turn's even when no user turn of that turn is seen (opencode
// drops an input of attachments alone from its turns): it is passed over. Only an error turn is
// not, as one may be stamped when it is read (codex's synthetic error of a refused turn/start), and
// passing over the run's own last error would read an earlier Done:.
//
// A page can start inside a person's later turn, whose input, stamped after pin, is on the page
// before: an answer counts when its own turn is stamped before pin, or else once a user turn of
// the session itself is seen before it (an answer stamped late, or not at all). found is false
// until then, so the caller reads back a page; atStart says turns begin at the transcript's first
// line, where nothing earlier can be such an input. A run that called tools for more lines than
// the pages read holds no user turn in them: its own answer, stamped before pin, is taken all the
// same.
func PinnedAnswer(turns []Turn, pin time.Time, atStart bool) (text string, found bool) {
	kept := TurnsBefore(turns, pin)
	var at int
	for {
		text, at, found = lastAnswerAt(kept)
		if !found {
			return "", false
		}
		ts, err := time.Parse(time.RFC3339Nano, kept[at].TS)
		if err == nil && ts.Before(pin) {
			return text, true
		}
		if err != nil || hasError(kept[at]) {
			break // stamped late, or not at all
		}
		kept = kept[:at] // a later turn's
	}
	if atStart {
		return text, true
	}
	for _, t := range kept[:at] {
		if t.Role == "user" && !t.Sidechain {
			return text, true
		}
	}
	return "", false
}

// LastAnswer is the text of the session's last answer in turns: of the last assistant turn of the
// session itself (not a subagent's) that holds anything but thinking, the run of text parts after
// its last other part (a tool call, a question, an error). A kind may put a whole response, tool
// calls and all, in one turn, and another puts each tool call in a turn of its own: either way a
// response that ended on a tool call has no answer after it, and what it said before the call is
// not taken for one. found is false when turns hold no such assistant turn at all (an older page
// may hold it); text is "" for one that ended without an answer.
func LastAnswer(turns []Turn) (text string, found bool) {
	text, _, found = lastAnswerAt(turns)
	return text, found
}

// hasError reports whether t holds an error part.
func hasError(t Turn) bool {
	for _, p := range t.Parts {
		if p.Kind == "error" {
			return true
		}
	}
	return false
}

// lastAnswerAt is LastAnswer and the index of the turn it was read from.
func lastAnswerAt(turns []Turn) (text string, at int, found bool) {
	for i := len(turns) - 1; i >= 0; i-- {
		t := turns[i]
		if t.Role != "assistant" || t.Sidechain {
			continue
		}
		if len(t.Parts) == 0 {
			if strings.TrimSpace(t.Text) == "" {
				continue
			}
			return t.Text, i, true
		}
		var texts []string
		seen := false
		for j := len(t.Parts) - 1; j >= 0; j-- {
			p := t.Parts[j]
			if p.Kind == "thinking" || (p.Kind == "text" && strings.TrimSpace(p.Text) == "") {
				continue
			}
			seen = true
			if p.Kind != "text" {
				break
			}
			texts = append([]string{p.Text}, texts...)
		}
		if !seen {
			continue
		}
		return strings.Join(texts, "\n"), i, true
	}
	return "", -1, false
}

// ClassifyAnswer is the head an answer is kept by (its first line that holds anything, at most
// store.AnswerHeadMax runes) and how it reads: done, partial, other, or unknown for no text. A
// heading mark before the word, emphasis or inline code marks around it (closed again before or
// after its colon), the case and a full-width colon are allowed: an agent that writes **Done:**
// or `Done`: said Done:. A quote (>), a list item or a code fence is not a declaration of the
// answer itself.
func ClassifyAnswer(text string) (head, class string) {
	for _, line := range strings.Split(text, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			head = s
			break
		}
	}
	if head == "" {
		return "", store.AnswerUnknown
	}
	if r := []rune(head); len(r) > store.AnswerHeadMax {
		head = string(r[:store.AnswerHeadMax])
	}
	norm := strings.TrimLeft(head, "# \t")
	open := norm[:len(norm)-len(strings.TrimLeft(norm, "*_`"))]
	if strings.Contains(open, "```") {
		return head, store.AnswerOther
	}
	norm = norm[len(open):]
	if startsWith(norm, "done", true, open) {
		return head, store.AnswerDone
	}
	for _, p := range partialHeads {
		if startsWith(norm, p.word, p.colon, open) {
			return head, store.AnswerPartial
		}
	}
	return head, store.AnswerOther
}

// startsWith reports whether s begins with word (any case), followed, when colon is set, by a
// colon. With colon, open is the emphasis or code marks before the word: they must be closed by the same
// marks, reversed, right after the word or right after its colon.
func startsWith(s, word string, colon bool, open string) bool {
	if len(s) < len(word) || !strings.EqualFold(s[:len(word)], word) {
		return false
	}
	if !colon {
		return true // a phrase, not a label: whatever marks come later
	}
	rest := s[len(word):]
	closeMarks := reverse(open)
	closed := open == ""
	if !closed && strings.HasPrefix(rest, closeMarks) {
		rest, closed = rest[len(closeMarks):], true
	}
	switch {
	case strings.HasPrefix(rest, ":"):
		rest = rest[1:]
	case strings.HasPrefix(rest, "："):
		rest = rest[len("："):]
	default:
		return false
	}
	return closed || strings.HasPrefix(rest, closeMarks)
}

func reverse(s string) string {
	b := []byte(s)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}
