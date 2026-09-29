package claude

import (
	"encoding/json"
	"slices"
	"testing"
)

func jsonlRows(rows ...string) [][]byte {
	out := make([][]byte, len(rows))
	for i, r := range rows {
		out[i] = []byte(r)
	}
	return out
}

func promptRow(promptID, text string) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "promptId": promptID, "message": map[string]any{"role": "user", "content": text}})
	return string(b)
}

func resultRow(promptID string) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "promptId": promptID, "message": map[string]any{
		"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "t", "content": "ok"}},
	}})
	return string(b)
}

func asstRow(msgID, blockType, text string) string {
	block := map[string]any{"type": blockType}
	switch blockType {
	case "text":
		block["text"] = text
	case "thinking":
		block["thinking"] = text
	default:
		block["id"], block["name"], block["input"] = "t", "Read", map[string]any{}
	}
	b, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"id": msgID, "content": []any{block}}})
	return string(b)
}

const attachmentRow = `{"type":"attachment","attachment":{"type":"queued_command"}}`

// The previous turn's answer and the current turn's first message, whose tool has run.
func twoTurns() [][]byte {
	return jsonlRows(
		promptRow("p1", "first"),
		asstRow("msg_1", "text", "Done."),
		promptRow("p2", "second"),
		asstRow("msg_2", "thinking", ""),
		asstRow("msg_2", "text", "Reading the file."),
		asstRow("msg_2", "tool_use", ""),
		resultRow("p2"),
	)
}

// While a message is written, the only rows of it the transcript can hold are the ones after
// the newest user row. Right after a tool result there are none, so nothing of an earlier
// message (or turn) is taken for it.
func TestPendingAssistantText(t *testing.T) {
	lines := twoTurns()
	if got := PendingAssistantText(lines); got != "" {
		t.Fatalf("right after a tool result got %q, want nothing", got)
	}
	lines = append(lines, jsonlRows(
		asstRow("msg_3", "thinking", ""),
		asstRow("msg_3", "text", "Searching. "),
		attachmentRow,
		asstRow("msg_3", "text", "Found it."),
	)...)
	if got := PendingAssistantText(lines); got != "Searching. Found it." {
		t.Fatalf("got %q, want both text rows of the message being written", got)
	}
}

// The messages of the turn that answers a prompt, newest first, and nothing from the turn before.
func TestTurnAssistantTexts(t *testing.T) {
	lines := append(twoTurns(), jsonlRows(
		asstRow("msg_3", "text", "The file says "),
		asstRow("msg_3", "text", "alpha."),
	)...)
	want := []string{"The file says alpha.", "Reading the file."}
	if got := TurnAssistantTexts(lines, "p2"); !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	// Without an id the newest prompt still bounds the turn.
	if got := TurnAssistantTexts(lines, ""); !slices.Equal(got, want) {
		t.Fatalf("with no prompt id got %q, want %q", got, want)
	}
	// A turn with no text yet reports none, not the previous turn's answer.
	fresh := jsonlRows(promptRow("p1", "first"), asstRow("msg_1", "text", "Done."), promptRow("p2", "second"))
	if got := TurnAssistantTexts(fresh, "p2"); len(got) != 0 {
		t.Fatalf("got %q for a turn that has written nothing", got)
	}
}

// A message is superseded once a newer prompt has come; the current prompt, an id the
// transcript does not show, and no id at all are not.
func TestPromptSuperseded(t *testing.T) {
	lines := twoTurns()
	if !PromptSuperseded(lines, "p1") {
		t.Fatal("p1 is followed by p2, but was not reported superseded")
	}
	for _, id := range []string{"p2", "p9", ""} {
		if PromptSuperseded(lines, id) {
			t.Fatalf("%q reported superseded", id)
		}
	}
}

// claude strips its memory citation tags from what it displays and keeps them in the
// transcript; DisplayedText makes the stored text comparable with the displayed stream.
func TestDisplayedTextStripsMemoryTags(t *testing.T) {
	stored := `As noted <cc-memory filenames="build.md">use the wrapper</cc-memory>, then <CC_MEMORY/>go.`
	if got, want := DisplayedText(stored), "As noted use the wrapper, then go."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// Only claude's tag names: other angle brackets are text.
	if got := DisplayedText("a <cc-memoryx> b <div>"); got != "a <cc-memoryx> b <div>" {
		t.Fatalf("got %q, want it untouched", got)
	}
}
