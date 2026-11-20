package opencode

import (
	"os"
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
	bindBroken.Store(false)
	t.Cleanup(credbind.ResetForTest)
}

func at(ms int64) time.Time { return time.UnixMilli(ms) }

func events(t *testing.T) []bindEvent {
	t.Helper()
	evs, ok := readBindEvents()
	if !ok {
		t.Fatal("evidence unreadable")
	}
	return evs
}

func bind(t *testing.T, route string, paid bool, ts ...int64) string {
	return bindingAt(events(t), route, paid, ts...)
}

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
	noteAccount(at(1500), acctOut)
	noteDaemonStart(at(5000), []string{opencodeKeyEnv + "=key-B"})
	noteAccount(at(5500), acctOut)
	for _, c := range []struct {
		route string
		ts    int64
		want  string
	}{
		{routes.RouteOpencodeZen, 3000, keyA},
		{routes.RouteOpencodeGo, 3000, keyA},
		{routes.RouteOpencodeZen, 9000, keyB},
		{routes.RouteOpencodeZen, 500, ""},   // before any daemon of ours
		{routes.RouteOpencodeZen, 1200, ""},  // before the life's first account reading
		{routes.RouteOpencodeZen, 5000, ""},  // the start's own millisecond is not told apart
		{routes.RouteOpencodeZen, 5300, ""},  // B's life, not yet read
		{routes.RouteAnthropicAPI, 3000, ""}, // not an opencode.ai route
	} {
		if got := bind(t, c.route, false, c.ts); got != c.want {
			t.Errorf("%s at %d = %q, want %q", c.route, c.ts, got, c.want)
		}
	}
}

func TestBindingKeylessFreeAndDoubt(t *testing.T) {
	bindT(t)
	noteDaemonStart(at(1000), []string{"X=1"})
	noteAccount(at(1100), acctOut)
	if got := bind(t, routes.RouteOpencodeZen, false, 2000); got != FreeBinding {
		t.Errorf("keyless zen = %q, want %q", got, FreeBinding)
	}
	if got := bind(t, routes.RouteOpencodeZen, true, 2000); got != "" {
		t.Errorf("a keyless call that reports a cost = %q, want none", got)
	}
	if got := bind(t, routes.RouteOpencodeGo, false, 2000); got != "" {
		t.Errorf("keyless go = %q, want none", got)
	}
	// An Agent flow touched the account at 3000: the account may pay from then on, however it ends,
	// and a later out reading does not undo it.
	noteAccount(at(3000), acctTouch)
	noteAccount(at(3500), acctOut)
	if got := bind(t, routes.RouteOpencodeZen, false, 2000); got != FreeBinding {
		t.Errorf("a call before the touch stays bound: %q", got)
	}
	if got := bind(t, routes.RouteOpencodeZen, false, 4000); got != "" {
		t.Errorf("a call after the touch = %q, want none", got)
	}
	// An adopted daemon proves nothing.
	noteDaemonUnknown(at(6000))
	if got := bind(t, routes.RouteOpencodeZen, false, 7000); got != "" {
		t.Errorf("adopted daemon = %q, want none", got)
	}
}

// A later reading never changes what an earlier call binds to, so a ledger retry folds the same
// row it folded first.
func TestBindingStableAsEvidenceGrows(t *testing.T) {
	bindT(t)
	noteDaemonStart(at(1000), []string{opencodeKeyEnv + "=k"})
	noteAccount(at(1100), acctOut)
	first := bind(t, routes.RouteOpencodeZen, false, 2000)
	noteAccount(at(3000), acctIn)
	noteDaemonStart(at(9000), []string{opencodeKeyEnv + "=k2"})
	if got := bind(t, routes.RouteOpencodeZen, false, 2000); got != first || first == "" {
		t.Errorf("binding moved from %q to %q", first, got)
	}
}

// A clock that went back, a start that shares the call's millisecond, and a run whose steps fall in
// different daemons prove nothing.
func TestBindingRefusesAmbiguousIntervals(t *testing.T) {
	bindT(t)
	noteDaemonStart(at(10000), []string{opencodeKeyEnv + "=A"})
	noteAccount(at(10100), acctOut)
	noteDaemonStart(at(9000), []string{opencodeKeyEnv + "=B"}) // stamped before A's: the clock went back
	noteAccount(at(9100), acctOut)
	for _, ts := range []int64{9500, 10050, 12000} {
		if got := bind(t, routes.RouteOpencodeZen, false, ts); got != "" {
			t.Errorf("at %d after a clock regression = %q, want none", ts, got)
		}
	}
	bindT(t)
	noteDaemonStart(at(1000), []string{opencodeKeyEnv + "=A"})
	noteAccount(at(1100), acctOut)
	noteDaemonStart(at(5000), []string{opencodeKeyEnv + "=A"}) // same key, restarted
	noteAccount(at(5100), acctOut)
	if got := bind(t, routes.RouteOpencodeZen, false, 2000, 3000); got == "" {
		t.Error("two steps of one daemon must bind")
	}
	if got := bind(t, routes.RouteOpencodeZen, false, 2000, 6000); got != "" {
		t.Errorf("a run across a restart = %q, want none even with the same key", got)
	}
}

// An unreadable or damaged file, or a boundary that could not be written, proves nothing.
func TestBindingNeedsIntactEvidence(t *testing.T) {
	bindT(t)
	noteDaemonStart(at(1000), []string{opencodeKeyEnv + "=A"})
	noteAccount(at(1100), acctOut)
	m := session.Meta{Kind: session.KindOpencode, Driver: session.DriverManaged}
	if ledgerBinder(m) == nil {
		t.Fatal("intact evidence gives a binder")
	}
	f, err := os.OpenFile(bindIntervalsPath(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("{not json\n")
	f.Close()
	if ledgerBinder(m) != nil {
		t.Error("a damaged line must disable binding")
	}
	bindT(t)
	noteDaemonStart(at(1000), []string{opencodeKeyEnv + "=A"})
	bindBroken.Store(true)
	if ledgerBinder(m) != nil {
		t.Error("a failed write must disable binding until the next daemon event")
	}
	noteDaemonStart(at(2000), []string{opencodeKeyEnv + "=A"})
	if ledgerBinder(m) == nil {
		t.Error("a written daemon event restores the evidence")
	}
}

// The file stays bounded, and the newest daemon event survives the trim with its readings.
func TestBindingTrimKeepsLiveDaemon(t *testing.T) {
	bindT(t)
	for i := 0; i < 2*bindIntervalsKeep; i++ {
		noteDaemonStart(at(1000+int64(i)*10), []string{opencodeKeyEnv + "=A"})
		noteAccount(at(1005+int64(i)*10), acctOut)
	}
	if got := bind(t, routes.RouteOpencodeZen, false, 900000); got == "" {
		t.Error("the live daemon lost its evidence to the trim")
	}
	if n := len(events(t)); n > bindIntervalsKeep+1 {
		t.Errorf("file holds %d events, bound is %d", n, bindIntervalsKeep)
	}
}

// The folded ledger turn carries the binding of its call's interval; a forked copy, a run across two
// daemons, a paid keyless call and sessions off the shared daemon carry none.
func TestV2TurnsBoundByInterval(t *testing.T) {
	bindT(t)
	keyA := credbind.Key(opencodeKeyEnv, "key-A")
	noteDaemonStart(at(1000), []string{opencodeKeyEnv + "=key-A"})
	noteAccount(at(1100), acctOut)
	noteDaemonStart(at(8000), []string{opencodeKeyEnv + "=key-B"})
	noteAccount(at(8100), acctOut)
	m := session.Meta{Kind: session.KindOpencode, Driver: session.DriverManaged}
	b := ledgerBinder(m)
	if b == nil {
		t.Fatal("a managed session on the shared daemon has a binder")
	}
	user := v2Msg{Type: "user"}
	user.Time.Created = 1500
	connOf := func(msgs []v2Msg, born int64) []string {
		var got []string
		for _, tn := range v2SessionTurnsBound(msgs, "/w", born, false, true, nil, b) {
			if tn.turn.Role == "assistant" {
				got = append(got, tn.turn.Connection)
			}
		}
		return got
	}
	step := routeMsg("opencode", true, 0.5) // created 2000, completed 3000
	if got := connOf([]v2Msg{user, step}, 0); len(got) != 1 || got[0] != keyA {
		t.Errorf("connection = %q, want %q", got, keyA)
	}
	if got := connOf([]v2Msg{user, step}, 2500); len(got) != 1 || got[0] != "" {
		t.Errorf("a copied message = %q, want none", got)
	}
	straddle := routeMsg("opencode", true, 0.5)
	straddle.Time.Completed = 9000
	if got := connOf([]v2Msg{user, straddle}, 0); len(got) != 1 || got[0] != "" {
		t.Errorf("a call spanning two daemons = %q, want none", got)
	}
	// A -> B -> A: both ends agree, a step in the middle does not.
	noteDaemonStart(at(12000), []string{opencodeKeyEnv + "=key-A"})
	noteAccount(at(12100), acctOut)
	s1, s2, s3 := routeMsg("opencode", true, 0.5), routeMsg("opencode", true, 0.5), routeMsg("opencode", true, 0.5)
	s1.Time.Created, s1.Time.Completed = 2000, 2500
	s2.Time.Created, s2.Time.Completed = 9000, 9500
	s3.Time.Created, s3.Time.Completed = 13000, 13500
	b = ledgerBinder(m)
	if got := connOf2(b, []v2Msg{user, s1, s2, s3}); len(got) != 1 || got[0] != "" {
		t.Errorf("a run A,B,A = %q, want none", got)
	}
	for name, meta := range map[string]session.Meta{
		"own route server": {Kind: session.KindOpencode, Driver: session.DriverManaged, Route: &routes.Selection{Route: routes.RouteBedrock}},
		"terminal":         {Kind: session.KindOpencode, Driver: session.DriverTUI},
	} {
		if ledgerBinder(meta) != nil {
			t.Errorf("%s: must not be bound from the shared daemon's evidence", name)
		}
	}
}

func connOf2(b bindFn, msgs []v2Msg) []string {
	var got []string
	for _, tn := range v2SessionTurnsBound(msgs, "/w", 0, false, true, nil, b) {
		if tn.turn.Role == "assistant" {
			got = append(got, tn.turn.Connection)
		}
	}
	return got
}
