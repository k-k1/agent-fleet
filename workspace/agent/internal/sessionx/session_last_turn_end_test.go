package sessionx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// lastTurnEndAt has to reach the LISTING, not merely exist as a function: GET /sessions is the
// route list_child_sessions reads (docs/log/89), and a helper wired nowhere is invisible to a
// unit test of the helper.
//
// The three cases are the whole contract. A turn that ended gives a time; an idle nobody can
// explain — a session mid-turn, or one just restarted — gives nothing rather than a plausible
// timestamp, because a parent polling "is my child done" reads any time here as evidence of
// completion (this is the distinction status.SessionStatus.TurnEnd exists to hold, docs/log/51).
func TestLastTurnEndRidesTheSessionsListing(t *testing.T) {
	isolateAgentState(t)

	const ended = "child_ended"
	for _, name := range []string{ended, "child_working", "child_silent"} {
		session.WriteMeta(session.Meta{
			Name: name, Kind: session.KindClaude, Dir: t.TempDir(),
			CreatedAt: "2026-09-09T10:00:00+09:00",
			Origin:    session.OriginSession, OriginSession: "parent1",
		})
	}
	metas := map[string]session.Meta{}
	for _, m := range session.ListMetas() {
		metas[m.Name] = m
	}
	// A real end of turn (claude's Stop hook / a managed driver's MarkTurnEnd) …
	status.PersistTurnEnd(session.UUID(metas[ended].Dir, ended), "idle")
	// … versus a turn in flight, which overwrites the marker with a plain state.
	status.Persist(session.UUID(metas["child_working"].Dir, "child_working"), "working")
	// … versus a session nothing has recorded anything for at all.

	srv := httptest.NewServer(http.HandlerFunc(HandleListSessions))
	defer srv.Close()
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET /sessions: %v", err)
	}
	defer res.Body.Close()
	var list struct{ Sessions []session.Session }
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		t.Fatalf("decode listing: %v", err)
	}

	got := map[string]string{}
	for _, s := range list.Sessions {
		got[s.Name] = s.LastTurnEndAt
	}
	if got[ended] == "" {
		t.Errorf("a child whose turn ended carries no lastTurnEndAt — the parent cannot tell it finished")
	}
	if got["child_working"] != "" {
		t.Errorf("a session mid-turn reported lastTurnEndAt=%q; an idle we cannot explain must stay empty", got["child_working"])
	}
	if got["child_silent"] != "" {
		t.Errorf("a session with no recorded turn reported lastTurnEndAt=%q", got["child_silent"])
	}
}
