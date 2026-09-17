package claude

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Which callers are allowed to read the cached (display) answer is a property of the WHOLE
// module, not of this package, and nothing in the type system carries it: both forms have
// the same signature, so using the wrong one compiles, passes every test and only shows up
// as a duplicated interruption, an early completion report, or a session stopped with a
// background agent still running.
//
// So the wiring is pinned here. Adding a caller means adding it to this list and saying why
// it only lights a badge.
var displayCallers = map[string]bool{
	"internal/agents/claude/claude.go":          true, // the session list's row
	"internal/sessionx/session_transcript.go":   true, // the chat header
	"internal/agents/claude/bg.go":              true, // the definitions themselves
	"internal/agents/claude/bg_display_test.go": true,
	"internal/agents/claude/bg_probe_test.go":   true,
}

var displayUse = regexp.MustCompile(`\b(?:claude\.)?(?:BackgroundWorkDisplay|SubagentBusyDisplay|subagentLogsDisplay|subagentBasesDisplay)\(`)

func TestOnlyBadgesReadTheCachedSubagentAnswer(t *testing.T) {
	root := moduleRoot(t)
	seen := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !displayUse.Match(b) {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		seen++
		if !displayCallers[rel] {
			t.Errorf("%s reads the cached subagent answer. It may be up to %s stale about a "+
				"running background agent, so it is for a badge only — if this is a decision, "+
				"use SubagentBusy / SubagentLogs / SubagentSnapshot, which always search",
				rel, subagentAbsentTTL)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen == 0 {
		t.Fatal("found no use of the display path at all — this test has stopped matching anything")
	}
}

// The other half: the three decisions must keep calling the searching form. A rename or a
// well-meant "these two look the same" cleanup would otherwise pass silently.
func TestTheThreeDecisionsStillSearch(t *testing.T) {
	root := moduleRoot(t)
	for _, c := range []struct{ file, call string }{
		{"internal/sessionx/session_delivery.go", "claude.SubagentReceivedSince("}, // misdelivery
		{"internal/chatx/chat_report_reconcile.go", "claude.SubagentBusy("},        // completion
		{"internal/chatx/chat_stop_after_turn.go", "collectReportSignals("},        // the armed stop
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
