package copilot

import (
	"strings"
	"testing"
)

// copilot spells an MCP tool `<server>-<tool>` with a HYPHEN — measured, not guessed:
// ~/.copilot/session-state holds `"toolName":"probe-structured_probe"` from a real call to a
// server named `probe`. The result arrives on a separate tool.execution_complete event, which
// is where the card can first be built (ADR 0069).
const genImageEvents = `{"type":"user.message","data":{"content":"draw a cat"},"timestamp":"2026-09-06T01:00:00Z"}
{"type":"assistant.turn_start","data":{"turnId":"0","model":"gpt-5-mini"},"timestamp":"2026-09-06T01:00:01Z"}
{"type":"tool.execution_start","data":{"toolCallId":"t1","toolName":"af_40ed9852-generate_image","arguments":{"prompt":"a cat"},"turnId":"0"},"timestamp":"2026-09-06T01:00:02Z"}
{"type":"tool.execution_start","data":{"toolCallId":"t2","toolName":"bash","arguments":{"command":"ls"},"turnId":"0"},"timestamp":"2026-09-06T01:00:03Z"}
{"type":"tool.execution_complete","data":{"toolCallId":"t1","success":true,"result":{"content":"{\"files\":[{\"path\":\"/home/u/.cache/agent-fleet/generated/sid/image-1.png\"}],\"provider\":\"codex\",\"warnings\":[\"size=1024x1024 requested, 1254x1254 produced\"]}"}},"timestamp":"2026-09-06T01:00:40Z"}
{"type":"tool.execution_complete","data":{"toolCallId":"t2","success":true,"result":{"content":"a.txt"}},"timestamp":"2026-09-06T01:00:41Z"}
{"type":"assistant.message","data":{"messageId":"m1","content":"できました","turnId":"0"},"timestamp":"2026-09-06T01:00:42Z"}
{"type":"assistant.turn_end","data":{"turnId":"0"},"timestamp":"2026-09-06T01:00:43Z"}
`

func TestCopilotGeneratedImageBecomesAUserFilePart(t *testing.T) {
	turns := parseEvents(writeFixture(t, genImageEvents))
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want a user turn and an assistant turn", len(turns))
	}
	parts := turns[1].Parts
	// generate_image, its card, bash, then the answer.
	if len(parts) != 4 {
		t.Fatalf("parts = %+v, want 4", parts)
	}
	if parts[0].Kind != "tool" || !strings.HasSuffix(parts[0].Tool, "generate_image") {
		t.Fatalf("parts[0] = %+v", parts[0])
	}
	if parts[1].Kind != "userfile" || len(parts[1].Files) != 1 ||
		parts[1].Files[0] != "/home/u/.cache/agent-fleet/generated/sid/image-1.png" {
		t.Fatalf("parts[1] = %+v, want the picture card", parts[1])
	}
	if parts[1].Caption != "size=1024x1024 requested, 1254x1254 produced" {
		t.Fatalf("caption = %q, want the warning", parts[1].Caption)
	}
	// The bug the collect-then-splice ordering exists to prevent: toolIdx holds positions in
	// this same slice, so a card inserted while the walk is still running would move the row
	// bash's output is about to be written to.
	if parts[2].Kind != "tool" || parts[2].Tool != "bash" || parts[2].Output != "a.txt" {
		t.Fatalf("parts[2] = %+v — the later tool's output landed on the wrong row", parts[2])
	}
	if parts[3].Kind != "text" {
		t.Fatalf("parts[3] = %+v", parts[3])
	}
}

// Another server's tool of the same name is not af's picture, and a refusal has no file.
func TestCopilotGeneratedImageIgnoresOtherResults(t *testing.T) {
	const events = `{"type":"assistant.turn_start","data":{"turnId":"0"},"timestamp":"2026-09-06T01:00:01Z"}
{"type":"tool.execution_start","data":{"toolCallId":"t1","toolName":"pictures-generate_image","arguments":{},"turnId":"0"},"timestamp":"2026-09-06T01:00:02Z"}
{"type":"tool.execution_complete","data":{"toolCallId":"t1","success":true,"result":{"content":"{\"files\":[{\"path\":\"/tmp/other.png\"}]}"}},"timestamp":"2026-09-06T01:00:03Z"}
{"type":"tool.execution_start","data":{"toolCallId":"t2","toolName":"af_40ed9852-generate_image","arguments":{},"turnId":"0"},"timestamp":"2026-09-06T01:00:04Z"}
{"type":"tool.execution_complete","data":{"toolCallId":"t2","success":false,"result":{"content":"画像を生成できませんでした: codex is not logged in"}},"timestamp":"2026-09-06T01:00:05Z"}
{"type":"assistant.turn_end","data":{"turnId":"0"},"timestamp":"2026-09-06T01:00:06Z"}
`
	for _, turn := range parseEvents(writeFixture(t, events)) {
		for _, p := range turn.Parts {
			if p.Kind == "userfile" {
				t.Fatalf("a picture card was produced for %+v", p)
			}
		}
	}
}
