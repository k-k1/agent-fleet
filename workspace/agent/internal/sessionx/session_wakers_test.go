package sessionx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/watch"
)

// The row names what it counts, so "waiting on a watch" can say which watch it is.
func TestFillWakersNamesTheHolders(t *testing.T) {
	old := watchWakers
	defer func() { watchWakers = old }()
	watchWakers = func(string) (watch.WakersSnapshot, bool) {
		return watch.WakersSnapshot{Watches: 1, Holders: []watch.Holder{{ID: "b:ci:github:o/r#1", Source: "github", Builtin: true}}}, true
	}
	var s session.Session
	fillWakers(&s, session.Meta{Name: "x"})
	if s.Wakers == nil || len(s.Wakers.Holders) != 1 || s.Wakers.Holders[0].ID != "b:ci:github:o/r#1" || s.Wakers.Holders[0].Source != "github" || !s.Wakers.Holders[0].Builtin {
		t.Fatalf("holders not on the row: %+v", s.Wakers)
	}
}
