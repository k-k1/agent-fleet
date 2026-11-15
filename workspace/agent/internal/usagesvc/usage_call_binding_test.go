package usagesvc

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// boundOpencode is opencode with a CallBinder that answers from a fixed table and counts its calls.
type boundOpencode struct {
	agents.Agent
	bindings map[string]string
	asked    *int
}

func (boundOpencode) RoutesPerCall() {}

func (b boundOpencode) CallBinding(route string) string {
	*b.asked++
	return b.bindings[route]
}

// A per-call kind's session rows that carry a route and no binding take the binding the kind's
// credentials prove for that route as the ledger records them (ADR 0138 D5b): the live check's
// opencode Zen rows had the route and connection null. A row with no route, one with a binding of
// its own, and a route the binder cannot prove stay as they were; the binder is asked once per
// route per batch.
func TestFoldBindsPerCallRows(t *testing.T) {
	UseTempUsageDir(t)
	old, _ := sessionx.RegisteredAgent(session.KindOpencode)
	asked := 0
	t.Cleanup(agentreg.SwapForTest(session.KindOpencode, boundOpencode{Agent: old,
		bindings: map[string]string{usagex.RouteOpencodeZen: "opencode-zen@free"}, asked: &asked}))
	zen := func(n int) transcript.Turn {
		tt := asst("opencode/big-pickle", 3, n, 0, 0, false)
		tt.Route = usagex.RouteOpencodeZen
		return tt
	}
	goTurn := asst("opencode-go/glm", 3, 4, 0, 0, false)
	goTurn.Route = usagex.RouteOpencodeGo
	m := session.Meta{Name: "ocfree01", Kind: session.KindOpencode}
	turns := []transcript.Turn{
		{Role: "user", Text: "1"}, zen(2),
		{Role: "user", Text: "2"}, zen(3),
		{Role: "user", Text: "3"}, goTurn,
		{Role: "user", Text: "4"}, asst("anthropic/claude-x", 3, 5, 0, 0, false),
	}
	usageFoldMu.Lock()
	st := readUsageFoldState()
	_, err := foldSessionUsageWithTurns(m, &st, turns, true)
	usageFoldMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, r := range usagex.ReadRows() {
		if r.Ref == m.Name {
			got[r.Route] = append(got[r.Route], r.Connection)
		}
	}
	if c := got[usagex.RouteOpencodeZen]; len(c) != 2 || c[0] != "opencode-zen@free" || c[1] != "opencode-zen@free" {
		t.Errorf("Zen rows: %v, want both bound", c)
	}
	if c := got[usagex.RouteOpencodeGo]; len(c) != 1 || c[0] != "" {
		t.Errorf("a route the binder cannot prove was bound: %v", c)
	}
	if c := got[""]; len(c) != 1 || c[0] != "" {
		t.Errorf("a row with no route was bound: %v", c)
	}
	if asked != 2 {
		t.Errorf("the binder was asked %d times, want once per route (2)", asked)
	}
	// A row with a binding of its own keeps it.
	recs := []usagex.Record{{Route: usagex.RouteOpencodeZen, Connection: "OPENCODE_API_KEY@k"}}
	stampCallBindings(m, recs)
	if recs[0].Connection != "OPENCODE_API_KEY@k" {
		t.Errorf("an own binding was replaced: %q", recs[0].Connection)
	}
}
