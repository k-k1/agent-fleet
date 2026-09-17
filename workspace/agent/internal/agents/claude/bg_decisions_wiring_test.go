package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The three decisions that rest on the subagent lookup, pinned by name. They were the
// reason a cached "no background agents" could not be allowed to spread (ADR 0087 decision
// 5), and they are still the reason subagentBases has to look at the disk every time. A
// rename or a well-meant "these two look the same" cleanup would otherwise pass silently.
//
// The fourth caller is not in this list because it is not in this module: WireLive's
// BackgroundBusy travels the wire into control-plane/session_activity.go, where the reaper
// stops the workspace on it. That one was missed by the first implementation of this
// decision, which is why it is written down here as well.
// The other half: the three decisions must keep calling the searching form. A rename or a
// well-meant "these two look the same" cleanup would otherwise pass silently.
func TestTheThreeDecisionsStillSearch(t *testing.T) {
	root := moduleRoot(t)
	for _, c := range []struct{ file, call string }{
		{"internal/sessionx/session_delivery.go", "claude.SubagentReceivedSince("}, // misdelivery
		{"internal/chatx/chat_report_reconcile.go", "claude.SubagentBusy("},        // completion
		{"internal/chatx/chat_stop_after_turn.go", "collectReportSignals("},        // the armed stop
		{"internal/agents/claude/claude.go", "BackgroundWork("},                    // the wire → the CP's reaper
	} {
		b, err := os.ReadFile(filepath.Join(root, c.file))
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if !strings.Contains(string(b), c.call) {
			t.Errorf("%s no longer calls %s — the safety path it carries has moved or gone", c.file, c.call)
		}
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for range 6 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("go.mod not found above the test's working directory")
	return ""
}
