package claude

import (
	"strings"
	"testing"
)

func jsonlRows(rows ...string) [][]byte {
	out := make([][]byte, len(rows))
	for i, r := range rows {
		out[i] = []byte(r)
	}
	return out
}

const (
	rowPrompt  = `{"type":"user","message":{"role":"user","content":"go"}}`
	rowResult  = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`
	rowM1Think = `{"type":"assistant","message":{"id":"msg_1","content":[{"type":"thinking","thinking":"","signature":"x"}]}}`
	rowM1Text  = `{"type":"assistant","message":{"id":"msg_1","content":[{"type":"text","text":"Reading the file."}]}}`
	rowM1Tool  = `{"type":"assistant","message":{"id":"msg_1","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]}}`
)

// While the second message streams, the newest text in the transcript is the first message's,
// even though that message's tool_use row and the tool result come after it.
func TestLatestAssistantTextSkipsTrailingNonTextRows(t *testing.T) {
	got := LatestAssistantText(jsonlRows(rowPrompt, rowM1Think, rowM1Text, rowM1Tool, rowResult))
	if got != "Reading the file." {
		t.Fatalf("got %q, want the first message's text", got)
	}
}

// A response with text on both sides of a server tool call is written as one row per text
// block. The message's text is all of them, in order.
func TestLatestAssistantTextJoinsTheMessagesTextRows(t *testing.T) {
	rows := jsonlRows(rowPrompt,
		`{"type":"assistant","message":{"id":"msg_2","content":[{"type":"text","text":"Searching. "}]}}`,
		`{"type":"assistant","message":{"id":"msg_2","content":[{"type":"server_tool_use","id":"s1","name":"web_search","input":{}}]}}`,
		`{"type":"attachment","attachment":{"type":"something"}}`,
		`{"type":"assistant","message":{"id":"msg_2","content":[{"type":"text","text":"Found it."}]}}`,
	)
	if got := LatestAssistantText(rows); got != "Searching. Found it." {
		t.Fatalf("got %q, want both text blocks of msg_2 joined", got)
	}
}

// Another message's row ends the gathering: its text is not part of the newest message.
func TestLatestAssistantTextStopsAtAnotherMessage(t *testing.T) {
	rows := jsonlRows(rowPrompt, rowM1Text,
		`{"type":"assistant","message":{"id":"msg_3","content":[{"type":"text","text":"Done."}]}}`,
	)
	if got := LatestAssistantText(rows); got != "Done." {
		t.Fatalf("got %q, want only msg_3's text", got)
	}
}

func TestLatestAssistantTextNoTextNearTheEnd(t *testing.T) {
	if got := LatestAssistantText(jsonlRows(rowPrompt)); got != "" {
		t.Fatalf("got %q with no assistant text at all", got)
	}
	rows := []string{rowPrompt, rowM1Text}
	for range latestTextScan {
		rows = append(rows, rowM1Tool)
	}
	if got := LatestAssistantText(jsonlRows(rows...)); got != "" {
		t.Fatalf("got %q from beyond the scan limit", strings.TrimSpace(got))
	}
}
