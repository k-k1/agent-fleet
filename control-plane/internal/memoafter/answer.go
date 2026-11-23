package memoafter

import (
	"strings"

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
}

// LastAnswer is the text of the session's last answer in turns: of the last assistant turn of the
// session itself (not a subagent's) that holds anything but thinking, the run of text parts after
// its last other part (a tool call, a question, an error). A kind may put a whole response, tool
// calls and all, in one turn, and another puts each tool call in a turn of its own: either way a
// response that ended on a tool call has no answer after it, and what it said before the call is
// not taken for one. found is false when turns hold no such assistant turn at all (an older page
// may hold it); text is "" for one that ended without an answer.
func LastAnswer(turns []Turn) (text string, found bool) {
	for i := len(turns) - 1; i >= 0; i-- {
		t := turns[i]
		if t.Role != "assistant" || t.Sidechain {
			continue
		}
		if len(t.Parts) == 0 {
			if strings.TrimSpace(t.Text) == "" {
				continue
			}
			return t.Text, true
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
		return strings.Join(texts, "\n"), true
	}
	return "", false
}

// ClassifyAnswer is the head an answer is kept by (its first line that holds anything, at most
// store.AnswerHeadMax runes) and how it reads: done, partial, other, or unknown for no text. A
// heading mark before the word, emphasis or code marks around it, the case and a full-width colon
// are allowed: an agent that writes **Done:** or `Done`: said Done:. A quote (>) or a list item is
// not a declaration of the answer itself.
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
	norm := strings.TrimLeft(strings.TrimLeft(head, "# \t"), "*_`")
	if startsWith(norm, "done", true) {
		return head, store.AnswerDone
	}
	for _, p := range partialHeads {
		if startsWith(norm, p.word, p.colon) {
			return head, store.AnswerPartial
		}
	}
	return head, store.AnswerOther
}

// startsWith reports whether s begins with word (any case), followed, when colon is set, by a
// colon, which emphasis or code marks closing the word may precede.
func startsWith(s, word string, colon bool) bool {
	if len(s) < len(word) || !strings.EqualFold(s[:len(word)], word) {
		return false
	}
	if !colon {
		return true
	}
	rest := strings.TrimLeft(s[len(word):], "*_`")
	return strings.HasPrefix(rest, ":") || strings.HasPrefix(rest, "：")
}
