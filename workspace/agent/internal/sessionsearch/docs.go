package sessionsearch

import (
	"strings"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// maxDocBytes caps one turn's indexed text. A pasted log or a generated file in a prompt can be
// megabytes; the index is for finding the conversation, and the head of such a turn is enough
// to find it by.
const maxDocBytes = 32 * 1024

// Doc is one indexed turn: what a person or the agent actually said in it.
type Doc struct {
	Idx  int    `json:"idx"`  // transcript.Turn.Idx — the position the mirror jumps to
	Role string `json:"role"` // "user" | "assistant"
	TS   string `json:"ts,omitempty"`
	Text string `json:"text"`
}

// DocsFromTurns picks out the conversation from a transcript. Kept: text, plans, and asked
// questions with their answers. Left out, by decision (ADR 0109 decision 3):
//   - tool calls and their output — the bulk of a transcript, mostly file contents and command
//     output, and the likeliest place for a secret to sit;
//   - thinking, subagent sidechains and delegation prompts — the agent talking to itself or to a
//     helper, not to the person;
//   - claude's compaction summaries — a restatement of turns that are indexed already.
func DocsFromTurns(turns []transcript.Turn) []Doc {
	var out []Doc
	for _, t := range turns {
		if t.Sidechain || t.Compact || t.CostOnly || (t.Role != "user" && t.Role != "assistant") {
			continue
		}
		text := turnText(t)
		if text == "" {
			continue
		}
		out = append(out, Doc{Idx: t.Idx, Role: t.Role, TS: t.TS, Text: clip(text, maxDocBytes)})
	}
	return out
}

func turnText(t transcript.Turn) string {
	if len(t.Parts) == 0 {
		return strings.TrimSpace(t.Text)
	}
	var segs []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			segs = append(segs, s)
		}
	}
	for _, p := range t.Parts {
		switch p.Kind {
		case "text":
			add(p.Text)
		case "plan":
			add(p.Plan)
		case "question":
			for _, q := range p.Questions {
				add(q.Question)
			}
			if !p.Declined {
				add(p.Answer)
			}
		}
	}
	return strings.Join(segs, "\n\n")
}

// clip cuts s to at most n bytes without splitting a UTF-8 sequence.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
