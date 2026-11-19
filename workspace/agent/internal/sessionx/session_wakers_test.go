package sessionx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/watch"
)

// The row names what it counts, so "waiting on a watch" can say which watch it is.
func TestFillWakersNamesTheHolders(t *testing.T) {
	oldW, oldH := watchWakers, watchHolders
	defer func() { watchWakers, watchHolders = oldW, oldH }()
	watchWakers = func(string) (int, int, int, bool) { return 1, 0, 0, true }
	watchHolders = func(string) []watch.Holder {
		return []watch.Holder{{ID: "b:ci:github:o/r#1", Source: "github", Builtin: true}}
	}
	var s session.Session
	fillWakers(&s, session.Meta{Name: "x"})
	if s.Wakers == nil || len(s.Wakers.Holders) != 1 || s.Wakers.Holders[0].ID != "b:ci:github:o/r#1" || s.Wakers.Holders[0].Source != "github" || !s.Wakers.Holders[0].Builtin {
		t.Fatalf("holders not on the row: %+v", s.Wakers)
	}
}
