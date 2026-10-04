package sessionsearch

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

func TestDocsFromTurnsKeepsOnlyTheConversation(t *testing.T) {
	turns := []transcript.Turn{
		{Role: "user", Idx: 1, Text: "fix the login bug"},
		{Role: "assistant", Idx: 2, Parts: []transcript.Part{
			{Kind: "thinking", Text: "secret reasoning"},
			{Kind: "tool", Tool: "Bash", Output: "AWS_SECRET=abc"},
			{Kind: "text", Text: "Found it in auth.go"},
			{Kind: "plan", Plan: "1. patch"},
			{Kind: "question", Questions: []transcript.Question{{Question: "Which DB?"}}, Answer: "Postgres"},
			{Kind: "question", Questions: []transcript.Question{{Question: "Declined?"}}, Answer: "boilerplate", Declined: true},
			{Kind: "delegation", Prompt: "subagent instructions"},
		}},
		{Role: "assistant", Idx: 3, Sidechain: true, Parts: []transcript.Part{{Kind: "text", Text: "sidechain"}}},
		{Role: "user", Idx: 4, Compact: true, Text: "summary of earlier turns"},
		{Role: "assistant", Idx: 5, Parts: []transcript.Part{{Kind: "tool", Output: "only a tool"}}},
	}
	docs := DocsFromTurns(turns)
	if len(docs) != 2 {
		t.Fatalf("docs = %+v", docs)
	}
	got := docs[1].Text
	for _, want := range []string{"Found it in auth.go", "1. patch", "Which DB?", "Postgres", "Declined?"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	for _, leak := range []string{"secret reasoning", "AWS_SECRET", "boilerplate", "subagent instructions"} {
		if strings.Contains(got, leak) {
			t.Errorf("indexed %q: %q", leak, got)
		}
	}
	if docs[0].Idx != 1 || docs[1].Idx != 2 || docs[1].Role != "assistant" {
		t.Fatalf("idx/role: %+v", docs)
	}
}

func TestDocsFromTurnsClipsHugeTurnsOnARuneBoundary(t *testing.T) {
	docs := DocsFromTurns([]transcript.Turn{{Role: "user", Text: strings.Repeat("認", maxDocBytes)}})
	if n := len(docs[0].Text); n > maxDocBytes || n < maxDocBytes-3 || !strings.HasSuffix(docs[0].Text, "認") {
		t.Fatalf("clipped to %d bytes", n)
	}
}
