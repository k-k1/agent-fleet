package sessionx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// A launch that restored the log leaves exactly one durable note per restore; a launch that
// reports nothing leaves none, and an unknown notice key is ignored rather than recorded.
func TestRecordLaunchNoticesNotesEachRestoreOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	m := session.Meta{Kind: session.KindClaude, Name: "restored", Dir: t.TempDir()}
	sid := session.UUID(m.Dir, m.Name)

	recordLaunchNotices(m, agents.LaunchPlan{})
	recordLaunchNotices(m, agents.LaunchPlan{Notices: []string{"something-else"}})
	if got := status.ReadRestoreNotes(sid); len(got) != 0 {
		t.Fatalf("notes without a restore = %v, want none", got)
	}

	recordLaunchNotices(m, agents.LaunchPlan{Notices: []string{agents.NoticeTranscriptRestored}})
	if got := status.ReadRestoreNotes(sid); len(got) != 1 {
		t.Fatalf("notes after one restore = %v, want 1", got)
	}
	recordLaunchNotices(m, agents.LaunchPlan{})
	if got := status.ReadRestoreNotes(sid); len(got) != 1 {
		t.Fatalf("a later plain launch changed the notes: %v", got)
	}
	recordLaunchNotices(m, agents.LaunchPlan{Notices: []string{agents.NoticeTranscriptRestored}})
	if got := status.ReadRestoreNotes(sid); len(got) != 2 {
		t.Fatalf("notes after a second restore = %v, want 2", got)
	}
}
