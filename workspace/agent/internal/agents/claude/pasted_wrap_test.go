package claude

import (
	"encoding/json"
	"testing"
)

func TestUnwrapPasted(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"logged user line", "\n\n<pasted_content id=\"c959\">\n承認した。\n\n下記を検討したい\n</pasted_content id=\"c959\">\n", "承認した。\n\n下記を検討したい"},
		{"queued prompt", "<pasted_content id=\"c959\">\n[agent-fleet:peer from=s1 intent=answer reply=none] done\nline 2\n</pasted_content id=\"c959\">", "[agent-fleet:peer from=s1 intent=answer reply=none] done\nline 2"},
		{"no wrapper", "plain\ntext", "plain\ntext"},
		{"ids differ", "<pasted_content id=\"a1\">\nx\n</pasted_content id=\"b2\">", "<pasted_content id=\"a1\">\nx\n</pasted_content id=\"b2\">"},
		{"text after the pair", "<pasted_content id=\"a1\">\nx\n</pasted_content id=\"a1\">\nmore", "<pasted_content id=\"a1\">\nx\n</pasted_content id=\"a1\">\nmore"},
		{"text before the pair", "see <pasted_content id=\"a1\">\nx\n</pasted_content id=\"a1\">", "see <pasted_content id=\"a1\">\nx\n</pasted_content id=\"a1\">"},
		{"two pairs", "<pasted_content id=\"a1\">\nx\n</pasted_content id=\"a1\">\n<pasted_content id=\"a1\">\ny\n</pasted_content id=\"a1\">", "<pasted_content id=\"a1\">\nx\n</pasted_content id=\"a1\">\n<pasted_content id=\"a1\">\ny\n</pasted_content id=\"a1\">"},
		{"odd id", "<pasted_content id=\"a 1\">\nx\n</pasted_content id=\"a 1\">", "<pasted_content id=\"a 1\">\nx\n</pasted_content id=\"a 1\">"},
		{"empty id", "<pasted_content id=\"\">\nx\n</pasted_content id=\"\">", "<pasted_content id=\"\">\nx\n</pasted_content id=\"\">"},
		{"opening tag only", "<pasted_content id=\"a1\">\nx", "<pasted_content id=\"a1\">\nx"},
	}
	for _, c := range cases {
		if got := unwrapPasted(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// wrappedLine is a user line as claude 2.1.330 logs a prompt typed as a bracketed paste.
func wrappedLine(t *testing.T, body string) string {
	t.Helper()
	content, _ := json.Marshal("\n\n<pasted_content id=\"c959\">\n" + body + "\n</pasted_content id=\"c959\">\n")
	return `{"type":"user","uuid":"u1","timestamp":"2026-11-20T23:50:27.397Z","message":{"role":"user","content":` + string(content) + `}}`
}

// Every reader of a logged prompt gets the paste without the wrapper: the transcript turn (what
// the Console renders, matches its pending echo and envelopes against, and what injection tagging
// compares), the prompt a stop puts back, and both mid-run queue forms.
func TestPastedPromptReachesReadersUnwrapped(t *testing.T) {
	const body = "[agent-fleet:peer from=s1 intent=answer reply=none] done\n\nline 2"

	turn, ok := parseTurn([]byte(wrappedLine(t, body)), 0)
	if !ok || turn.Text != body || len(turn.Parts) != 1 || turn.Parts[0].Text != body {
		t.Errorf("user line: got (%q, %v), want %q", turn.Text, ok, body)
	}

	if got, ok := userPromptText([]byte(wrappedLine(t, body))); !ok || got != body {
		t.Errorf("stop restore: got (%q, %v), want %q", got, ok, body)
	}

	queued, _ := json.Marshal("<pasted_content id=\"c959\">\n" + body + "\n</pasted_content id=\"c959\">")
	att := `{"type":"attachment","attachment":{"type":"queued_command","prompt":` + string(queued) + `,"commandMode":"prompt"}}`
	if turn, ok := parseTurn([]byte(att), 0); !ok || turn.Text != body {
		t.Errorf("queued_command: got (%q, %v), want %q", turn.Text, ok, body)
	}

	qop := func(op string) []byte {
		return []byte(`{"type":"queue-operation","operation":"` + op + `","content":` + string(queued) + `}`)
	}
	if got := CollectQueued([][]byte{qop("enqueue")}); len(got) != 1 || got[0] != body {
		t.Errorf("queue: got %q, want [%q]", got, body)
	}
	if got := CollectQueued([][]byte{qop("enqueue"), qop("remove")}); len(got) != 0 {
		t.Errorf("queue remove: got %q, want empty", got)
	}
}
