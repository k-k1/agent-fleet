package opencode

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func bindT(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	credbind.ResetForTest()
	t.Cleanup(credbind.ResetForTest)
}

func at(ms int64) time.Time { return time.UnixMilli(ms) }

// A call made on key A keeps A after the key is replaced with B (the daemon restarted), and the
// replacement's calls get B: the binding comes from the interval, not from the credentials at fold
// time.
func TestBindingKeepsKeyOfItsInterval(t *testing.T) {
	bindT(t)
	keyA := credbind.Key(opencodeKeyEnv, "key-A")
	keyB := credbind.Key(opencodeKeyEnv, "key-B")
	if keyA == "" || keyA == keyB {
		t.Fatalf("test needs distinct bindings: %q %q", keyA, keyB)
	}
	noteDaemonStart(at(1000), []string{"X=1", opencodeKeyEnv + "=key-A"})
	noteAccountObs(at(1500), false)
	noteDaemonStart(at(5000), []string{opencodeKeyEnv + "=key-B"})
	noteAccountObs(at(5500), false)
	evs := readBindEvents()
	for _, c := range []struct {
		route string
		ts    int64
		want  string
	}{
		{routes.RouteOpencodeZen, 3000, keyA},
		{routes.RouteOpencodeGo, 3000, keyA},
		{routes.RouteOpencodeZen, 9000, keyB},
		{routes.RouteOpencodeZen, 500, ""},    // before any daemon of ours
		{routes.RouteOpencodeZen, 5000, keyB}, // the start instant belongs to the new daemon
		{routes.RouteAnthropicAPI, 3000, ""},  // not an opencode.ai route
		{routes.RouteOpencodeZen, 1200, keyA}, // before the first observation: the one after bounds it
	} {
		if got := bindingAt(evs, c.route, c.ts); got != c.want {
			t.Errorf("%s at %d = %q, want %q", c.route, c.ts, got, c.want)
		}
	}
}

func TestBindingKeylessFreeAndDoubt(t *testing.T) {
	bindT(t)
	noteDaemonStart(at(1000), []string{"X=1"})
	noteAccountObs(at(1100), false)
	evs := readBindEvents()
	if got := bindingAt(evs, routes.RouteOpencodeZen, 2000); got != FreeBinding {
		t.Errorf("keyless zen = %q, want %q", got, FreeBinding)
	}
	if got := bindingAt(evs, routes.RouteOpencodeGo, 2000); got != "" {
		t.Errorf("keyless go = %q, want none", got)
	}
	// The account signed in at 3000: calls after it have two possible payers.
	noteAccountObs(at(3000), true)
	evs = readBindEvents()
	if got := bindingAt(evs, routes.RouteOpencodeZen, 2000); got != "" {
		t.Errorf("call before an observed sign-in is bounded by it, not proven free: %q", got)
	}
	if got := bindingAt(evs, routes.RouteOpencodeZen, 4000); got != "" {
		t.Errorf("signed-in account = %q, want none", got)
	}
	// An adopted daemon proves nothing, and neither does a daemon never observed.
	noteDaemonUnknown(at(6000))
	if got := bindingAt(readBindEvents(), routes.RouteOpencodeZen, 7000); got != "" {
		t.Errorf("adopted daemon = %q, want none", got)
	}
	bindT(t)
	noteDaemonStart(at(1000), []string{opencodeKeyEnv + "=k"})
	if got := bindingAt(readBindEvents(), routes.RouteOpencodeZen, 2000); got != "" {
		t.Errorf("no account observation = %q, want none", got)
	}
}

// The folded ledger turn carries the binding of its call's interval, and a run whose steps fall
// in different intervals carries none.
func TestV2TurnsBoundByInterval(t *testing.T) {
	bindT(t)
	keyA := credbind.Key(opencodeKeyEnv, "key-A")
	noteDaemonStart(at(1000), []string{opencodeKeyEnv + "=key-A"})
	noteAccountObs(at(1100), false)
	noteDaemonStart(at(8000), []string{opencodeKeyEnv + "=key-B"})
	noteAccountObs(at(8100), false)
	m := session.Meta{Kind: session.KindOpencode, Driver: session.DriverManaged}
	bind := ledgerBinder(m)
	if bind == nil {
		t.Fatal("a managed session on the shared daemon has a binder")
	}
	user := v2Msg{Type: "user"}
	user.Time.Created = 1500
	step := routeMsg("opencode", true, 0.5) // created 2000, completed 3000
	turns := v2SessionTurnsBound([]v2Msg{user, step}, "/w", 0, false, true, nil, bindFn(bind))
	var got []string
	for _, tn := range turns {
		if tn.turn.Role == "assistant" {
			got = append(got, tn.turn.Connection)
		}
	}
	if len(got) != 1 || got[0] != keyA {
		t.Errorf("connection = %q, want %q", got, keyA)
	}
	straddle := routeMsg("opencode", true, 0.5)
	straddle.Time.Completed = 9000
	for _, tn := range v2SessionTurnsBound([]v2Msg{user, straddle}, "/w", 0, false, true, nil, bindFn(bind)) {
		if tn.turn.Role == "assistant" && tn.turn.Connection != "" {
			t.Errorf("a call spanning two daemons has binding %q, want none", tn.turn.Connection)
		}
	}
	// Sessions that did not run on the shared daemon get no binder.
	own := session.Meta{Kind: session.KindOpencode, Driver: session.DriverManaged, Route: &routes.Selection{Route: routes.RouteBedrock}}
	if ledgerBinder(own) != nil {
		t.Error("a session with its own route server must not be bound from the shared daemon's evidence")
	}
}
