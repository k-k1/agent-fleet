package main

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// Every caller of tmuxx.LiveSessionNames (shutdown, cleanup, the sessions list) looks its keys
// up as live[m.Name]. If it ever starts returning prefixed names every lookup silently misses
// and each of them treats live sessions as dead.
//
// Pins the contract read-only against the real tmux. It creates no session: doing so would
// pollute the fleet's claude_* namespace and put a ghost session in the Console.
func TestLiveSessionNamesCarryNoPrefix(t *testing.T) {
	live := tmuxx.LiveSessionNames()
	if len(live) == 0 {
		t.Skip("no fleet session in tmux - the contract cannot be observed")
	}
	for name := range live {
		if strings.HasPrefix(name, session.TmuxPrefix) {
			t.Fatalf("LiveSessionNames returned the prefixed %q - live[m.Name] never matches, "+
				"so every live session reads as dead", name)
		}
		if session.TmuxName(name) != session.TmuxPrefix+name {
			t.Fatalf("TmuxName(%q) disagrees with assembling the prefix by hand", name)
		}
	}
}
