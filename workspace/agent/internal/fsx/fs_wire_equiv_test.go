package fsx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/wiretest"
)

// --- (1) HandleLineMarks (Console: LineMarks) ---

type lineMarksIn struct {
	Added    []int
	Modified []int
	Deleted  []int
}

func TestWireEquivLineMarks(t *testing.T) {
	inputs := []lineMarksIn{
		{Added: []int{1, 2}, Modified: []int{5}, Deleted: []int{9}},
		// All three production paths (emptyMarks / untracked / diff) initialise with make
		// or []int{}, so it is never nil there. A nil slice is `null` and an empty one is
		// `[]` — different, so measure the empty-slice shape explicitly.
		{Added: []int{}, Modified: []int{}, Deleted: []int{}},
	}
	got := wiretest.AssertEquiv(t, "HandleLineMarks", inputs,
		func(in lineMarksIn) any { // old (copy of the map literal in fs_git.go)
			return map[string]any{"added": in.Added, "modified": in.Modified, "deleted": in.Deleted}
		},
		func(in lineMarksIn) any {
			return lineMarksWire{Added: in.Added, Modified: in.Modified, Deleted: in.Deleted}
		})
	t.Logf("comparison mode: %s", got)
}
