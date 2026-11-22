package sessionx

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
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

// The handlers must actually return the tie-broken order: a call to the sort deleted from
// either one passes the helper test above.
func tiedMetas(prefix, dir string, archived bool) {
	for _, c := range []struct{ n, at string }{{"b", "2026-10-01T10:00:00Z"}, {"a", "2026-10-01T10:00:00Z"}, {"c", "2026-10-01T11:00:00Z"}, {"d", "2026-10-01T09:00:00Z"}} {
		session.WriteMeta(session.Meta{Name: prefix + c.n, Dir: dir, Kind: session.KindCodex, CreatedAt: c.at, Archived: archived})
	}
}

func rowNames(t *testing.T, body []byte, prefix string) []string {
	t.Helper()
	var b struct {
		Sessions []struct {
			Name string `json:"name"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range b.Sessions {
		if strings.HasPrefix(s.Name, prefix) {
			out = append(out, strings.TrimPrefix(s.Name, prefix))
		}
	}
	return out
}

func TestHandleListArchived_TiedCreatedAtOrderIsDeterministic(t *testing.T) {
	tiedMetas("sorttie_arch_", t.TempDir(), true)
	rec := httptest.NewRecorder()
	HandleListArchived(rec, httptest.NewRequest("GET", "/sessions/archived", nil))
	if got := rowNames(t, rec.Body.Bytes(), "sorttie_arch_"); !reflect.DeepEqual(got, []string{"c", "a", "b", "d"}) {
		t.Fatalf("order = %v, want [c a b d]", got)
	}
}

func TestHandleListSessions_TiedCreatedAtOrderIsDeterministic(t *testing.T) {
	fakeTmuxListSessionsAlive(t, "")
	tiedMetas("sorttie_live_", t.TempDir(), false)
	rec := httptest.NewRecorder()
	HandleListSessions(rec, httptest.NewRequest("GET", "/sessions", nil))
	if got := rowNames(t, rec.Body.Bytes(), "sorttie_live_"); !reflect.DeepEqual(got, []string{"c", "a", "b", "d"}) {
		t.Fatalf("order = %v, want [c a b d]", got)
	}
}
