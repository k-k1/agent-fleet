package opencode

import (
	"math"
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
	oldStamp, oldGen, oldNow := clockStamp, genSnap.Load(), wallNow
	clockStamp = func() (string, int64, bool) { return "boot", time.Now().UnixMilli(), true }
	wallNow = time.Now
	t.Cleanup(func() { clockStamp, wallNow = oldStamp, oldNow; genSnap.Store(oldGen) })
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

// fill writes n alternating sign-in readings far enough apart to be kept, starting at at.
func fillReadings(n int, at int64, x, y string) int64 {
	step := 2 * touchCoalesce.Milliseconds()
	for i := 0; i < n; i++ {
		id := x
		if i%2 == 1 {
			id = y
		}
		acctIn_(at, id, 1)
		at += step
	}
	return at
}

// A trim inside one life keeps the start-of-life reading and the first touch: a call a key paid
// before the first touch stays bound, and an out reading kept after the cut never reads as the
// start-of-life one.
func TestBindingLongLifeKeepsTheStartOfLife(t *testing.T) {
	bindT(t)
	x, y := accountBinding("acct-X"), accountBinding("acct-Y")
	start(1000, 1, "key-A")
	acct(1100, acctOut, 1)
	acct(3000, acctTouch, 1)
	at := fillReadings(3*bindIntervalsKeep, 10000, x, y)
	acct(at+10, acctOut, 1)
	if n := len(events(t)); n > bindIntervalsKeep+1 {
		t.Errorf("file holds %d events", n)
	}
	if got := bind(t, routes.RouteOpencodeZen, false, 2000); got == "" {
		t.Error("a call a key paid before the first touch lost its binding to the trim")
	}
	for _, ts := range []int64{at + 1000000, at + 100} {
		if got := bind(t, routes.RouteOpencodeZen, false, ts); got != "" {
			t.Errorf("a call after the out reading at %d = %q, want none", ts, got)
		}
	}
}

// An account event whose clock stamp was unusable (an unknown bound) is not turned into a finite
// one by the trim: account calls up to the end of what was dropped stay unbound.
func TestBindingTrimKeepsAnUnknownClockBound(t *testing.T) {
	bindT(t)
	x, y := accountBinding("acct-X"), accountBinding("acct-Y")
	start(1000, 1, "")
	acctIn_(1100, x, 1)
	old := clockStamp
	clockStamp = func() (string, int64, bool) { return "", 0, false }
	stampUp = 70000
	noteAccount(atMs(70000), acctTouch, 1) // Reg, Floor unknown
	clockStamp = old
	fillReadings(3*bindIntervalsKeep, 200000, x, y)
	for _, e := range events(t) {
		if e.Kind == evStart && e.AcctCarry != math.MaxInt64 {
			t.Errorf("AcctCarry = %d, want it saturated", e.AcctCarry)
		}
	}
}

// A reading that shows the clock going back is recorded even though the same credential was
// already settled.
func TestBindingSettledReadingGoingBackIsKept(t *testing.T) {
	bindT(t)
	x := accountBinding("acct-X")
	start(1000, 1, "")
	acctIn_(1100, x, 1)
	n := len(events(t))
	acctIn_(1100+10*touchCoalesce.Milliseconds(), x, 1) // settled: nothing new to learn
	if got := len(events(t)); got != n {
		t.Fatalf("settled repeat was written: %d -> %d", n, got)
	}
	stampUp = 1100 + 20*touchCoalesce.Milliseconds()
	noteAccountID(atMs(1500), acctIn, x, 1) // wall clock far behind the uptime
	last := events(t)
	if got := last[len(last)-1]; !got.Reg {
		t.Errorf("the reading that went back was dropped: %+v", got)
	}
}

// An account event whose clock stamp was unusable concerns its own life only: the key calls of the
// life before it survive a trim, and a healthy later daemon is bound as usual.
func TestBindingUnstampedEventDoesNotReachLaterLives(t *testing.T) {
	bindT(t)
	x, y := accountBinding("acct-X"), accountBinding("acct-Y")
	start(1000, 1, "key-A")
	acct(1100, acctOut, 1)
	acct(3000, acctTouch, 1)
	old := clockStamp
	clockStamp = func() (string, int64, bool) { return "", 0, false }
	stampUp = 3000 + 2*touchCoalesce.Milliseconds()
	noteAccount(atMs(stampUp), acctTouch, 1)
	clockStamp = old
	at := fillReadings(2*bindIntervalsKeep, 500000, x, y) // trims the life
	start(at+1000, 2, "key-B")
	acct(at+1100, acctOut, 2)
	if got := bind(t, routes.RouteOpencodeZen, false, at+5000); got == "" {
		t.Error("a healthy later daemon was not bound")
	}
	if got := bind(t, routes.RouteOpencodeZen, false, 2000); got == "" {
		t.Error("a key call before the first touch was lost after the trim")
	}
}

// An out reading that shows the clock going back is kept even right behind a touch, and puts the
// life in doubt.
func TestBindingOutReadingGoingBackIsKept(t *testing.T) {
	bindT(t)
	start(1000, 1, "")
	acct(1100, acctOut, 1)
	acct(50000, acctTouch, 1)
	stampUp = 50000 + 10*touchCoalesce.Milliseconds()
	noteAccount(atMs(2000), acctOut, 1) // the wall clock is far behind the uptime
	evs := events(t)
	if got := evs[len(evs)-1]; got.Acct != acctOut || !got.Reg {
		t.Fatalf("the out reading that went back was dropped: %+v", got)
	}
	if got := bind(t, routes.RouteOpencodeZen, false, 1500); got != "" {
		t.Errorf("a call in a life whose clock moved = %q, want none", got)
	}
}

func TestBindingUnstampedEventStaysInItsLife(t *testing.T) {
	bindT(t)
	start(1000, 1, "key-A")
	acct(1100, acctOut, 1)
	acct(3000, acctTouch, 1)
	old := clockStamp
	clockStamp = func() (string, int64, bool) { return "", 0, false }
	stampUp = 3000 + 2*touchCoalesce.Milliseconds()
	noteAccount(atMs(stampUp), acctTouch, 1)
	clockStamp = old
	b := stampUp + 1000
	start(b, 2, "key-B")
	acct(b+100, acctOut, 2)
	if got := bind(t, routes.RouteOpencodeZen, false, b+5000); got == "" {
		t.Error("a healthy later daemon was not bound")
	}
}

// A repeated out reading whose stamp is merely missing adds nothing: the call a key paid in the
// life stays bound.
func TestBindingUnstampedRepeatedOutIsNotKept(t *testing.T) {
	bindT(t)
	start(1000, 1, "key-A")
	acct(1100, acctOut, 1)
	if bind(t, routes.RouteOpencodeZen, false, 3000) == "" {
		t.Fatal("test needs a bound call")
	}
	old := clockStamp
	clockStamp = func() (string, int64, bool) { return "", 0, false }
	noteAccount(atMs(5000), acctOut, 1)
	clockStamp = old
	if n := len(events(t)); n != 2 {
		t.Errorf("a repeated out with no stamp was written: %d events", n)
	}
	if got := bind(t, routes.RouteOpencodeZen, false, 3000); got == "" {
		t.Error("the call a key paid lost its binding")
	}
}

// A run of out readings that each show the clock moving cannot grow the file without bound.
func TestBindingRunOfMovedOutReadingsStaysBounded(t *testing.T) {
	bindT(t)
	start(1000, 1, "")
	acct(1100, acctOut, 1)
	for i := int64(0); i < 3*bindIntervalsKeep; i++ {
		stampUp = 2000 + i*100000 // the uptime runs ahead of the wall clock
		noteAccount(atMs(1200+i), acctOut, 1)
	}
	if n := len(events(t)); n > bindIntervalsKeep+1 {
		t.Errorf("file holds %d events, want at most %d", n, bindIntervalsKeep+1)
	}
}

// A process held up between reading the wall clock and the uptime does not read as a clock that was
// set: a repeated out reading is still dropped and the key call stays bound.
func TestBindingHeldUpStampIsNotAClockChange(t *testing.T) {
	bindT(t)
	real := func() (string, int64, bool) { return "boot", time.Now().UnixMilli(), true }
	wallNow = time.Now
	base := time.Now().UnixMilli() - 100000
	start(base, 1, "key-A")
	stampUp = base + 100
	noteAccount(time.UnixMilli(base+100), acctOut, 1)
	callAt := base + 5000
	if bind(t, routes.RouteOpencodeZen, false, callAt) == "" {
		t.Fatal("test needs a bound call")
	}
	clockStamp = func() (string, int64, bool) { // the process stalls before the uptime read
		time.Sleep(2100 * time.Millisecond)
		return real()
	}
	noteAccount(time.Now(), acctOut, 1)
	if n := len(events(t)); n != 2 {
		t.Errorf("a repeated out from a held-up sample was kept: %d events", n)
	}
	if got := bind(t, routes.RouteOpencodeZen, false, callAt); got == "" {
		t.Error("the call a key paid lost its binding")
	}
}

// A caller held up between taking the observation instant and reaching the stamp (not scheduled for
// seconds) does not read as a clock that was set: the old instant is paired with the uptime of that
// instant, not the current one, so a repeated out reading is still dropped.
func TestBindingStallBeforeTheCallIsNotAClockChange(t *testing.T) {
	bindT(t)
	base := time.Now().UnixMilli() - 100000
	start(base, 1, "key-A")
	stampUp = base + 100
	noteAccount(time.UnixMilli(base+100), acctOut, 1)
	callAt := base + 5000
	if bind(t, routes.RouteOpencodeZen, false, callAt) == "" {
		t.Fatal("test needs a bound call")
	}
	// real clocks from here on
	clockStamp = func() (string, int64, bool) { return "boot", time.Now().UnixMilli(), true }
	wallNow, sinceInstant = time.Now, time.Since
	noteAccount(time.Now().Add(-3*time.Second), acctOut, 1) // the stall happened before the call
	if n := len(events(t)); n != 2 {
		t.Errorf("a repeated out from a stalled caller was kept: %d events", n)
	}
	if got := bind(t, routes.RouteOpencodeZen, false, callAt); got == "" {
		t.Error("the call a key paid lost its binding")
	}
}
