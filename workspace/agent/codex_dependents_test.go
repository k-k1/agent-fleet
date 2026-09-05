package main

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// The shared app-server's "fold up when demand hits zero" decision rests entirely on this
// count being right (docs/log/27 §7.1). A bug that keeps returning 0 is silent, and shows
// up in the worst possible shape: a running codex TUI session's conversation suddenly stops.
func TestCountCodexTUISessions(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())

	write := func(name, kind, driver string, archived bool) {
		session.WriteMeta(session.Meta{Name: name, Kind: kind, Driver: driver, Archived: archived})
	}
	write("cx-live", session.KindCodex, "", false) // TUI and live -> counted
	write("cx-live2", session.KindCodex, session.DriverTUI, false)
	write("cx-dead", session.KindCodex, "", false) // TUI but not present in tmux
	write("cx-managed", session.KindCodex, session.DriverManaged, false)
	write("cx-archived", session.KindCodex, "", true)
	write("cl-live", session.KindClaude, "", false) // another kind does not use the app-server

	live := map[string]bool{"cx-live": true, "cx-live2": true, "cx-managed": true, "cl-live": true}
	if got := countCodexTUISessions(live); got != 2 {
		t.Fatalf("countCodexTUISessions = %d, want 2 (only codex sessions that are TUI and live)", got)
	}
	if got := countCodexTUISessions(nil); got != 0 {
		t.Fatalf("countCodexTUISessions(nil) = %d, want 0", got)
	}
}

// countCodexTUISessions assumes live's keys are session names with the prefix stripped and
// looks them up as live[m.Name]. If tmuxx.LiveSessionNames ever starts returning prefixed
// names the count silently becomes a constant 0, and the zero-demand decision pulls the
// backend out from under a live TUI.
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
			t.Fatalf("LiveSessionNames returned the prefixed %q - countCodexTUISessions' "+
				"live[m.Name] never matches, so TUI sessions stop counting as demand", name)
		}
		if session.TmuxName(name) != session.TmuxPrefix+name {
			t.Fatalf("TmuxName(%q) disagrees with assembling the prefix by hand", name)
		}
	}
}
