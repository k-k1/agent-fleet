package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The three record shapes one close event is known to arrive in (measured on 2.1.252):
// a plain user message, the queue-operation pair written when it lands mid-turn, and the
// queued_command attachment produced when that queued prompt is consumed. Each must close
// the agent on its own — the detector keys on the <task-id> marker they share.
func closeUser(id string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":"<task-notification>\n<task-id>%s</task-id>\n<status>completed</status>\n</task-notification>"}}`,
		time.Now().Format(time.RFC3339), id)
}

func closeQueueOp(id string) string {
	return fmt.Sprintf(`{"type":"queue-operation","operation":"enqueue","timestamp":%q,"content":"<task-notification>\n<task-id>%s</task-id>\n<status>completed</status>\n</task-notification>"}`,
		time.Now().Format(time.RFC3339), id)
}

func closeAttachment(id string) string {
	return fmt.Sprintf(`{"type":"attachment","timestamp":%q,"attachment":{"type":"queued_command","prompt":"<task-notification>\n<task-id>%s</task-id>\n<status>completed</status>\n</task-notification>"}}`,
		time.Now().Format(time.RFC3339), id)
}

// launch is the Agent tool_result claude writes when a background agent starts.
func launch(id string, at time.Time) string {
	return fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","content":[{"type":"text","text":"Async agent launched successfully. (internal metadata)\nagentId: %s (internal ID)\nThe agent is working in the background."}]}]}}`,
		at.Format(time.RFC3339), id)
}

// TestBackgroundAgentsRunning drives the launch/notification pairing against a fixtured
// CLAUDE_CONFIG_DIR. The point of the pairing is that it does NOT depend on how recently
// anything was written, so none of these cases touch mtimes.
func TestBackgroundAgentsRunning(t *testing.T) {
	const sid = "2222bbbb-3333-5ccc-8ddd-444455556666"
	const idA = "a1feec722532bbfd4"
	const idB = "b2864cc346a808feb"

	// setup points ConfigDir() at a temp root and returns the session's main transcript
	// path, matched by jsonlPaths' projects/* glob.
	setup := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", root)
		p := filepath.Join(root, "projects", "proj-a", sid+".jsonl")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write := func(t *testing.T, p string, lines ...string) {
		t.Helper()
		var b []byte
		for _, l := range lines {
			b = append(append(b, l...), '\n')
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	appendLines := func(t *testing.T, p string, lines ...string) {
		t.Helper()
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		for _, l := range lines {
			if _, err := f.WriteString(l + "\n"); err != nil {
				t.Fatal(err)
			}
		}
	}
	now := time.Now()

	t.Run("empty sid", func(t *testing.T) {
		setup(t)
		if BackgroundAgentsRunning("") {
			t.Fatal("empty sid must be false")
		}
	})

	t.Run("no transcript", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", root)
		if BackgroundAgentsRunning(sid) {
			t.Fatal("missing transcript must be false")
		}
	})

	t.Run("launched, never reported back", func(t *testing.T) {
		p := setup(t)
		write(t, p, launch(idA, now))
		if !BackgroundAgentsRunning(sid) {
			t.Fatal("an unreported launch must read busy")
		}
	})

	t.Run("launch closed by each notification shape", func(t *testing.T) {
		for name, closer := range map[string]func(string) string{
			"user": closeUser, "queue-operation": closeQueueOp, "attachment": closeAttachment,
		} {
			t.Run(name, func(t *testing.T) {
				p := setup(t)
				write(t, p, launch(idA, now), closer(idA))
				if BackgroundAgentsRunning(sid) {
					t.Fatalf("%s notification must close the agent", name)
				}
			})
		}
	})

	t.Run("one of two still open", func(t *testing.T) {
		p := setup(t)
		write(t, p, launch(idA, now), launch(idB, now), closeUser(idA))
		if !BackgroundAgentsRunning(sid) {
			t.Fatal("a second agent still running must read busy")
		}
	})

	t.Run("a notification for another agent does not close ours", func(t *testing.T) {
		p := setup(t)
		write(t, p, launch(idA, now), closeUser(idB))
		if !BackgroundAgentsRunning(sid) {
			t.Fatal("closing a different id must leave ours open")
		}
	})

	// The scan is incremental (each poll folds in only the appended bytes), so the cases
	// that matter most are the ones crossing a poll boundary.
	t.Run("close appended after an earlier poll is seen", func(t *testing.T) {
		p := setup(t)
		write(t, p, launch(idA, now))
		if !BackgroundAgentsRunning(sid) {
			t.Fatal("first poll: must read busy")
		}
		appendLines(t, p, closeUser(idA))
		if BackgroundAgentsRunning(sid) {
			t.Fatal("second poll must fold in the close record")
		}
	})

	t.Run("launch appended after an earlier poll is seen", func(t *testing.T) {
		p := setup(t)
		write(t, p, launch(idA, now), closeUser(idA))
		if BackgroundAgentsRunning(sid) {
			t.Fatal("first poll: must read idle")
		}
		appendLines(t, p, launch(idB, now))
		if !BackgroundAgentsRunning(sid) {
			t.Fatal("second poll must fold in the new launch")
		}
	})

	t.Run("a shorter file re-reads from the top", func(t *testing.T) {
		p := setup(t)
		write(t, p, launch(idA, now), launch(idB, now), closeUser(idA), closeUser(idB))
		if BackgroundAgentsRunning(sid) {
			t.Fatal("first poll: must read idle")
		}
		// A different conversation now lives at this path (a fork pinned onto the same
		// sid). Folding its records onto the previous open set would be nonsense.
		write(t, p, launch(idA, now))
		if !BackgroundAgentsRunning(sid) {
			t.Fatal("replaced transcript must be re-read whole")
		}
	})

	// The abandon ceiling: a launch whose close record will never come (claude killed
	// mid-agent) must not pin the badge on forever. Its agent log is the clock.
	t.Run("abandoned launch with no agent log", func(t *testing.T) {
		p := setup(t)
		write(t, p, launch(idA, now.Add(-bgAgentAbandonTTL-time.Minute)))
		if BackgroundAgentsRunning(sid) {
			t.Fatal("a launch older than the ceiling, with no agent log, must clear")
		}
	})

	t.Run("old launch still writing is not abandoned", func(t *testing.T) {
		p := setup(t)
		write(t, p, launch(idA, now.Add(-bgAgentAbandonTTL-time.Minute)))
		log := bgAgentLog(p, idA)
		if err := os.MkdirAll(filepath.Dir(log), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(log, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !BackgroundAgentsRunning(sid) {
			t.Fatal("an agent still appending must survive the ceiling")
		}
	})
}
