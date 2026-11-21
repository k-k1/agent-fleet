package mcpx

import (
	"strings"
	"testing"
)

func TestSpawnPromptForReplyContract(t *testing.T) {
	old := mcpPeerMessagingEnabled
	t.Cleanup(func() { mcpPeerMessagingEnabled = old })
	mcpPeerMessagingEnabled = true
	off := false

	const marker = "When this task is finished, send exactly one message back"
	cases := []struct {
		name       string
		prompt     string
		reportBack *bool
		peer       bool
		wantLine   bool
	}{
		{"default appends the line", "rebase onto develop", nil, true, true},
		{"own send_to_peer_session contract is kept", "Review it, then send ONE message with send_to_peer_session(name=\"p\", intent=\"answer\") holding every finding", nil, true, false},
		{"report_back false", "rebase onto develop", &off, true, false},
		{"empty prompt", "  ", nil, true, false},
		{"peer messaging off", "rebase onto develop", nil, false, false},
	}
	for _, c := range cases {
		mcpPeerMessagingEnabled = c.peer
		got := spawnPromptFor("parent", c.prompt, c.reportBack)
		if has := strings.Contains(got, marker); has != c.wantLine {
			t.Errorf("%s: line present = %v, want %v: %q", c.name, has, c.wantLine, got)
		}
		if !c.wantLine && got != c.prompt {
			t.Errorf("%s: prompt changed: %q", c.name, got)
		}
	}
}
