package claude

import (
	"encoding/json"
	"testing"
	"time"
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

// stampedRow sets a row's transcript timestamp.
func stampedRow(row string, at time.Time) string {
	var m map[string]any
	_ = json.Unmarshal([]byte(row), &m)
	m["timestamp"] = at.UTC().Format(time.RFC3339Nano)
	b, _ := json.Marshal(m)
	return string(b)
}

// A message's rows are the ones between the user row it answers — the newest one written
// before it started — and its own tool results. An earlier message that said the same thing
// sits before that boundary, and the next message after the following one.
func TestMessageTextFrom(t *testing.T) {
	t0 := time.Date(2026, 9, 30, 1, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	lines := jsonlRows(
		stampedRow(promptRow("p1", "go"), at(0)),
		stampedRow(asstRow("msg_1", "text", "Done."), at(2)),
		stampedRow(asstRow("msg_1", "tool_use", ""), at(3)),
		stampedRow(resultRow("p1"), at(4)),
		stampedRow(asstRow("msg_2", "thinking", ""), at(6)),
		stampedRow(asstRow("msg_2", "text", "Done. "), at(7)),
		stampedRow(asstRow("msg_2", "text", "Next."), at(8)),
		stampedRow(asstRow("msg_2", "tool_use", ""), at(9)),
		stampedRow(resultRow("p1"), at(10)),
		stampedRow(asstRow("msg_3", "text", "Later."), at(12)),
	)
	for _, c := range []struct {
		start int
		want  string
	}{
		{1, "Done."},       // msg_1 answers the prompt
		{5, "Done. Next."}, // msg_2 answers msg_1's tool result, and stops at its own
		{11, "Later."},
	} {
		got, ok := MessageTextFrom(lines, at(c.start))
		if !ok || got != c.want {
			t.Errorf("started at +%ds: got %q (ok=%v), want %q", c.start, got, ok, c.want)
		}
	}
	// A message whose rows have not landed yet has nothing after its boundary.
	if got, ok := MessageTextFrom(lines[:5], at(5)); !ok || got != "" {
		t.Errorf("msg_2 not landed: got %q (ok=%v), want nothing", got, ok)
	}
	// No user row written before start, or no timestamps at all: no boundary to go by.
	if _, ok := MessageTextFrom(lines, t0.Add(-time.Second)); ok {
		t.Error("a start before every row still found a boundary")
	}
	if _, ok := MessageTextFrom(jsonlRows(promptRow("p1", "go"), asstRow("msg_1", "text", "Done.")), at(5)); ok {
		t.Error("rows without timestamps still found a boundary")
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
