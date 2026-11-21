package mcpx

import (
	"strings"
	"testing"
)

// The appended line must stay for every prompt that qualifies, and must defer to a reply
// contract the caller wrote instead of overriding it.
func TestSpawnPromptForReplyContract(t *testing.T) {
	old := mcpPeerMessagingEnabled
	t.Cleanup(func() { mcpPeerMessagingEnabled = old })
	off := false

	const deferral = "Unless the instructions above already say how and to whom to report back"
	const line = "send exactly one message back"
	cases := []struct {
		name       string
		prompt     string
		reportBack *bool
		peer       bool
		wantLine   bool
	}{
		{"default appends the line", "rebase onto develop", nil, true, true},
		{"tool merely mentioned keeps the line", "review the send_to_peer_session implementation", nil, true, true},
		{"own tool contract still gets the deferring line", "send ONE message with send_to_peer_session(name=\"p\", intent=\"answer\") holding every finding", nil, true, true},
		{"natural-language contract still gets the deferring line", "when done, send the parent one message with every finding in full", nil, true, true},
		{"report_back false", "rebase onto develop", &off, true, false},
		{"empty prompt", "  ", nil, true, false},
		{"peer messaging off", "rebase onto develop", nil, false, false},
	}
	for _, c := range cases {
		mcpPeerMessagingEnabled = c.peer
		got := spawnPromptFor("parent", c.prompt, c.reportBack)
		if has := strings.Contains(got, line); has != c.wantLine {
			t.Errorf("%s: line present = %v, want %v: %q", c.name, has, c.wantLine, got)
		}
		if c.wantLine {
			if !strings.HasPrefix(got, c.prompt) || !strings.Contains(got, deferral) {
				t.Errorf("%s: the line must follow the prompt and defer to its contract: %q", c.name, got)
			}
		} else if got != c.prompt {
			t.Errorf("%s: prompt changed: %q", c.name, got)
		}
	}
}
