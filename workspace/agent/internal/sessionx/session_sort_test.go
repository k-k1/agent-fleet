package sessionx

import (
	"reflect"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestSortSessionsNewestFirstTieBreak(t *testing.T) {
	mk := func(n, c string) session.Session { return session.Session{Name: n, CreatedAt: c} }
	names := func(s []session.Session) []string {
		o := []string{}
		for _, x := range s {
			o = append(o, x.Name)
		}
		return o
	}
	one := []session.Session{mk("b", "t1"), mk("a", "t1"), mk("c", "t2")}
	two := []session.Session{mk("a", "t1"), mk("c", "t2"), mk("b", "t1")}
	sortSessionsNewestFirst(one)
	sortSessionsNewestFirst(two)
	if !reflect.DeepEqual(names(one), names(two)) || !reflect.DeepEqual(names(one), []string{"c", "a", "b"}) {
		t.Fatalf("got %v and %v", names(one), names(two))
	}
}
