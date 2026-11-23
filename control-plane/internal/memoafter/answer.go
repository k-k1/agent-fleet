package memoafter

import (
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The last answer of a delivery's root session (issue #4876): what the run said it did, read from
// the Agent's transcript after the delivery closed.

// partialHeads are the first words of a run that stopped part way, as the unattended implementer
// is told to begin its last answer. Matched like Done:, after the same clean-up.
var partialHeads = []string{"partial:", "held:", "deferred:", "too large:", "stopped at part"}

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
// session itself (not a subagent's) that holds any text, the run of text parts that ends with its
// last one. A kind may put a whole response, tool calls and all, in one turn, so the answer is only
// what it said after its last tool call. "" when no turn holds text.
func LastAnswer(turns []Turn) string {
	for i := len(turns) - 1; i >= 0; i-- {
		t := turns[i]
		if t.Role != "assistant" || t.Sidechain {
			continue
		}
		last := -1
		for j := len(t.Parts) - 1; j >= 0; j-- {
			if t.Parts[j].Kind == "text" && strings.TrimSpace(t.Parts[j].Text) != "" {
				last = j
				break
			}
		}
		if last < 0 {
			if len(t.Parts) == 0 && strings.TrimSpace(t.Text) != "" {
				return t.Text
			}
			continue
		}
		var texts []string
		for j := last; j >= 0; j-- {
			switch t.Parts[j].Kind {
			case "text":
				texts = append([]string{t.Parts[j].Text}, texts...)
				continue
			case "thinking":
				continue
			}
			break
		}
		return strings.Join(texts, "\n")
	}
	return ""
}

// ClassifyAnswer is the head an answer is kept by (its first line that holds anything, at most
// store.AnswerHeadMax runes) and how it reads: done, partial, other, or unknown for no text.
// Markdown emphasis and code marks are ignored, as are a heading, quote or list mark before the
// word, and the case: an agent that writes **Done:** or `Done:` said Done:.
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
	norm := strings.Map(func(r rune) rune {
		switch r {
		case '*', '`', '_':
			return -1
		case '：': // a full-width colon, as an IME writes it
			return ':'
		}
		return r
	}, head)
	norm = strings.ToLower(strings.TrimLeft(norm, "#>-+ \t"))
	if strings.HasPrefix(norm, "done:") {
		return head, store.AnswerDone
	}
	for _, p := range partialHeads {
		if strings.HasPrefix(norm, p) {
			return head, store.AnswerPartial
		}
	}
	return head, store.AnswerOther
}
