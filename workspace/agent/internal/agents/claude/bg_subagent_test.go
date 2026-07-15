package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSubagentBusy drives the in-process background-agent detector against a
// fixtured CLAUDE_CONFIG_DIR: a fresh agent-*.jsonl (regular subagent or Workflow
// agent) means busy; anything stale, non-agent, or missing means idle.
func TestSubagentBusy(t *testing.T) {
	const sid = "1111aaaa-2222-5bbb-8ccc-333344445555"

	// setup points ConfigDir() at a temp root and returns the session's subagents dir
	// (projects/<proj>/<sid>/subagents), matched by SubagentBusy's projects/* glob.
	setup := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", root)
		return filepath.Join(root, "projects", "proj-a", sid, "subagents")
	}
	// write creates a log and, when age > 0, backdates its mtime to simulate a
	// subagent that stopped appending that long ago.
	write := func(t *testing.T, path string, age time.Duration) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if age > 0 {
			ts := time.Now().Add(-age)
			if err := os.Chtimes(path, ts, ts); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("empty sid", func(t *testing.T) {
		setup(t)
		if SubagentBusy("") {
			t.Fatal("empty sid must be false")
		}
	})

	t.Run("no subagents dir", func(t *testing.T) {
		setup(t)
		if SubagentBusy(sid) {
			t.Fatal("missing dir must be false")
		}
	})

	t.Run("stale regular subagent", func(t *testing.T) {
		sub := setup(t)
		write(t, filepath.Join(sub, "agent-abc.jsonl"), 5*time.Minute)
		if SubagentBusy(sid) {
			t.Fatal("stale log must be false")
		}
	})

	t.Run("fresh regular subagent", func(t *testing.T) {
		sub := setup(t)
		write(t, filepath.Join(sub, "agent-abc.jsonl"), 0)
		if !SubagentBusy(sid) {
			t.Fatal("fresh regular subagent log must be true")
		}
	})

	t.Run("fresh workflow agent", func(t *testing.T) {
		sub := setup(t)
		write(t, filepath.Join(sub, "workflows", "wf_xyz", "agent-1.jsonl"), 0)
		if !SubagentBusy(sid) {
			t.Fatal("fresh workflow agent log must be true")
		}
	})

	t.Run("fresh non-agent file ignored", func(t *testing.T) {
		sub := setup(t)
		// journal.jsonl is fresh but not an agent-*.jsonl; the only agent log is stale.
		write(t, filepath.Join(sub, "workflows", "wf_xyz", "journal.jsonl"), 0)
		write(t, filepath.Join(sub, "agent-old.jsonl"), 10*time.Minute)
		if SubagentBusy(sid) {
			t.Fatal("no fresh agent-*.jsonl must be false")
		}
	})
}
