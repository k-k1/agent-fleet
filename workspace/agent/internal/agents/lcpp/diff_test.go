package lcpp

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
)

// TestNewMessagesSincePlainAppend is the simple, no-compaction case: everything past the
// original length is new.
func TestNewMessagesSincePlainAppend(t *testing.T) {
	before := []harness.Message{
		{Role: harness.RoleUser, Content: "hi"},
		{Role: harness.RoleAssistant, Content: "hello"},
	}
	after := append(append([]harness.Message(nil), before...),
		harness.Message{Role: harness.RoleUser, Content: "how are you"},
		harness.Message{Role: harness.RoleAssistant, Content: "fine"},
	)
	got := newMessagesSince(before, after)
	if len(got) != 2 || got[0].Content != "how are you" || got[1].Content != "fine" {
		t.Fatalf("got = %+v", got)
	}
}

// TestNewMessagesSinceCompactionInsertion is the case the naive "everything past len(before)"
// approach gets wrong: a boundary message is INSERTED into the middle of before (matching
// loop.go's compactPreservingLastRoundTrip — toSummarize and the preserved round trip are
// before's own content, unmodified, with one new system message spliced between them). Only
// the boundary must come back as new, never the messages on either side of it.
func TestNewMessagesSinceCompactionInsertion(t *testing.T) {
	before := []harness.Message{
		{Role: harness.RoleUser, Content: "q1"},
		{Role: harness.RoleAssistant, Content: "a1"},
		{Role: harness.RoleUser, Content: "q2"},
		{Role: harness.RoleAssistant, Content: "a2"},
	}
	boundary := harness.Message{Role: harness.RoleSystem, Content: "[lcpp compaction summary]\nrecap"}
	// Insert the boundary between q1/a1 and q2/a2 (as if lastAssistant landed at index 2), then
	// append the genuinely new round trip Run produced afterward.
	after := []harness.Message{
		before[0], before[1],
		boundary,
		before[2], before[3],
		{Role: harness.RoleUser, Content: "q3"},
		{Role: harness.RoleAssistant, Content: "a3"},
	}
	got := newMessagesSince(before, after)
	if len(got) != 3 {
		t.Fatalf("got %d new messages, want 3 (boundary, q3, a3): %+v", len(got), got)
	}
	if got[0].Role != harness.RoleSystem || got[0].Content != boundary.Content {
		t.Fatalf("got[0] = %+v, want the boundary", got[0])
	}
	if got[1].Content != "q3" || got[2].Content != "a3" {
		t.Fatalf("got[1:] = %+v, want q3/a3", got[1:])
	}
}

// TestNewMessagesSinceEmptyBefore is the "first turn ever" case: every message in after is
// new.
func TestNewMessagesSinceEmptyBefore(t *testing.T) {
	after := []harness.Message{{Role: harness.RoleUser, Content: "hi"}, {Role: harness.RoleAssistant, Content: "hello"}}
	got := newMessagesSince(nil, after)
	if len(got) != 2 {
		t.Fatalf("got = %+v, want both messages new", got)
	}
}
