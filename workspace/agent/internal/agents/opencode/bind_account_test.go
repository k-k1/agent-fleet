package opencode

import (
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// quietMs is a gap that clears the quiet period behind a flow.
var quietMs = accountQuiet.Milliseconds() + 1000

func acctIn_(ms int64, id string, gen int) {
	stampUp = ms
	noteAccountID(atMs(ms), acctIn, id, gen)
}

// A keyless daemon whose account was observed signed in pays its cost calls with the account; a
// free-looking zen call is not claimed, and a daemon with a key is ambiguous.
func TestBindingAccountUsesGivenBinding(t *testing.T) {
	bindT(t)
	x := accountBinding("acct-X")
	start(1000, 1, "")
	acctIn_(1100, x, 1)
	for _, c := range []struct {
		route string
		paid  bool
		ts    int64
		want  string
	}{
		{routes.RouteOpencodeZen, true, 2000, x},
		{routes.RouteOpencodeGo, false, 2000, x},
		{routes.RouteOpencodeGo, true, 2000, x},
		{routes.RouteOpencodeZen, false, 2000, ""}, // may be a free model: not the account's
		{routes.RouteOpencodeZen, true, 1100, ""},  // the observation's own millisecond
		{routes.RouteOpencodeZen, true, 1050, ""},  // before the observation
	} {
		if got := bind(t, c.route, c.paid, c.ts); got != c.want {
			t.Errorf("%s paid=%v at %d = %q, want %q", c.route, c.paid, c.ts, got, c.want)
		}
	}
	start(9000, 2, "key-B") // a key in the daemon's environment: the payer is ambiguous
	acctIn_(9100, x, 2)
	if got := bind(t, routes.RouteOpencodeZen, true, 9500); got != "" {
		t.Errorf("daemon with a key and a signed-in account = %q, want none", got)
	}
}

// Acceptance: a call made on account X keeps X after the account was replaced with Y; calls in the
// window where the replacement may have happened are unbound.
func TestBindingAccountReplacedKeepsOld(t *testing.T) {
	bindT(t)
	x, y := accountBinding("acct-X"), accountBinding("acct-Y")
	start(1000, 1, "")
	acctIn_(1100, x, 1)
	acct(3000, acctTouch, 1) // the sign-in flow that replaces X
	acctIn_(3000+quietMs, y, 1)
	for _, c := range []struct {
		ts   int64
		want string
	}{
		{2000, x},
		{3000, ""},
		{3500, ""},
		{3000 + quietMs - 1, ""},
		{3000 + quietMs + 500, y},
	} {
		if got := bind(t, routes.RouteOpencodeGo, true, c.ts); got != c.want {
			t.Errorf("at %d = %q, want %q", c.ts, got, c.want)
		}
	}
	// A call spanning the replacement is unbound.
	if got := bind(t, routes.RouteOpencodeGo, true, 2000, 3500); got != "" {
		t.Errorf("spanning call = %q", got)
	}
}

// An observation that follows a flow closely proves nothing: the flow may still be finishing on
// its own, and the credential it saves has not been seen.
func TestBindingAccountNeedsQuietAfterFlow(t *testing.T) {
	bindT(t)
	x := accountBinding("acct-X")
	start(1000, 1, "")
	acct(2000, acctTouch, 1)
	acctIn_(2500, x, 1)
	if got := bind(t, routes.RouteOpencodeGo, true, 5000); got != "" {
		t.Errorf("observation right behind a touch = %q, want none", got)
	}
	acctIn_(2000+quietMs, x, 1)
	if got := bind(t, routes.RouteOpencodeGo, true, 2000+quietMs+100); got != x {
		t.Errorf("observation after the quiet period = %q, want %q", got, x)
	}
}

// Repeated readings and bursts of touches do not grow the file, and a coalesced touch still ends
// the quiet period it extends.
func TestBindingAccountEventsAreCoalesced(t *testing.T) {
	bindT(t)
	x := accountBinding("acct-X")
	start(1000, 1, "")
	for i := int64(0); i < 5; i++ {
		acctIn_(1100+i*15000, x, 1)
	}
	if n := len(events(t)); n != 2 {
		t.Errorf("repeated in readings wrote %d events, want 2", n)
	}
	acct(100000, acctTouch, 1)
	acct(110000, acctTouch, 1) // within touchCoalesce of the written one
	if n := len(events(t)); n != 3 {
		t.Errorf("a burst of touches wrote %d events, want 3", n)
	}
	// The unwritten touch at 110000 may be finishing a flow: the quiet period counts from the
	// written one plus the coalescing window.
	acctIn_(100000+accountQuiet.Milliseconds()-1, x, 1)
	if got := bind(t, routes.RouteOpencodeGo, true, 100000+accountQuiet.Milliseconds()+500); got != "" {
		t.Errorf("= %q, want none", got)
	}
}

func TestBumpSeenMaxNeverLowers(t *testing.T) {
	bindT(t)
	var wg sync.WaitGroup
	for i := int64(1); i <= 200; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); bumpSeenMax(i) }()
	}
	wg.Wait()
	bumpSeenMax(5)
	if got := seenMaxAt.Load(); got != 200 {
		t.Errorf("seenMaxAt = %d, want 200", got)
	}
}

// The credential going away again ends its proof: a call after an out reading is not the account's,
// and the out reading is not taken for the start-of-life one.
func TestBindingAccountOutAfterInEndsProof(t *testing.T) {
	bindT(t)
	x := accountBinding("acct-X")
	start(1000, 1, "")
	acctIn_(1100, x, 1)
	acct(5000, acctOut, 1)
	if n := len(events(t)); n != 3 {
		t.Fatalf("out after in was not kept: %d events", n)
	}
	for _, c := range []struct {
		ts   int64
		want string
	}{{3000, x}, {6000, ""}} {
		if got := bind(t, routes.RouteOpencodeGo, true, c.ts); got != c.want {
			t.Errorf("at %d = %q, want %q", c.ts, got, c.want)
		}
	}
	if got := bind(t, routes.RouteOpencodeZen, false, 6000); got != "" {
		t.Errorf("a free-looking call after the account went away = %q, want none (out came after a sign-in)", got)
	}
}

// A second touch whose clock stamp is unusable puts only the calls it may follow in doubt; a call
// that a key paid before the first touch keeps its binding.
func TestBindingUnstampedLaterTouchSparesEarlierCalls(t *testing.T) {
	bindT(t)
	start(1000, 1, "key-A")
	acct(1100, acctOut, 1)
	acct(3000, acctTouch, 1)
	old := clockStamp
	clockStamp = func() (string, int64, bool) { return "", 0, false }
	stampUp = 3000 + 2*touchCoalesce.Milliseconds()
	noteAccount(atMs(stampUp), acctTouch, 1)
	clockStamp = old
	keyA := bind(t, routes.RouteOpencodeZen, false, 2000)
	if keyA == "" {
		t.Error("a call before the first touch lost its key binding")
	}
	if got := bind(t, routes.RouteOpencodeZen, false, 3500); got != "" {
		t.Errorf("a call after the touches = %q, want none", got)
	}
}

// One long life does not grow the file without bound, and the calls whose evidence was dropped are
// not bound.
func TestBindingLongLifeStaysBounded(t *testing.T) {
	bindT(t)
	x, y := accountBinding("acct-X"), accountBinding("acct-Y")
	start(1000, 1, "")
	step := 2 * touchCoalesce.Milliseconds()
	at := int64(10000)
	for i := 0; i < 3*bindIntervalsKeep; i++ {
		id := x
		if i%2 == 1 {
			id = y
		}
		acctIn_(at, id, 1)
		at += step
	}
	if n := len(events(t)); n > bindIntervalsKeep+1 {
		t.Errorf("file holds %d events, want at most %d", n, bindIntervalsKeep+1)
	}
	if got := bind(t, routes.RouteOpencodeGo, true, 20000); got != "" {
		t.Errorf("a call whose evidence was dropped = %q, want none", got)
	}
}

// End to end through the readers: the Console view the Agent reads from the daemon is what records
// the credential, and the ledger binder of a session on the shared daemon returns X for a call made
// on X after the account was replaced by Y.
func TestLedgerBinderAccountReplacement(t *testing.T) {
	bindT(t)
	oldQ := accountQuiet
	accountQuiet = 0
	t.Cleanup(func() { accountQuiet = oldQ })
	d := newFakeDaemon(t)
	now := func() int64 { stampUp = time.Now().UnixMilli(); return stampUp }
	genSnap.Store(1)
	base := now() - 100000
	start(base, 1, "")
	stampUp = base + 1
	recordSessionProcess(atMs(base+1), "s", procShared, nil, oauthState{}, time.Time{})
	read := func() {
		time.Sleep(3 * time.Millisecond)
		now()
		invalidateOAuthStatus()
		oauthStatus()
		time.Sleep(3 * time.Millisecond)
	}
	d.setConns([]map[string]any{{"type": "credential", "id": "con_X", "label": "a"}})
	read()
	tx := now()
	time.Sleep(5 * time.Millisecond)
	d.setConns([]map[string]any{{"type": "credential", "id": "con_Y", "label": "b"}})
	now()
	noteAccount(time.Now(), acctTouch, 1) // the flow that replaces it
	tTouch := now()
	time.Sleep(5 * time.Millisecond)
	read()
	ty := now()
	bindFor := func(ts int64) string {
		now()
		b := ledgerBinder(session.Meta{Name: "s"})
		if b == nil {
			t.Fatal("no binder")
		}
		return b(routes.RouteOpencodeGo, true, ts)
	}
	if got, want := bindFor(tx), accountBinding("con_X"); got != want {
		t.Errorf("call on X = %q, want %q", got, want)
	}
	if got := bindFor(tTouch + 1); got != "" {
		t.Errorf("call inside the replacement = %q, want none", got)
	}
	if got, want := bindFor(ty), accountBinding("con_Y"); got != want {
		t.Errorf("call on Y = %q, want %q", got, want)
	}
}
