package opencode

import (
	"os"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func onShared(name string, ms int64) {
	stampUp = ms
	recordSessionProcess(atMs(ms), name, procShared, nil, oauthState{}, time.Time{})
}

// onPane launches a pane at ms; the account was observed signed out at outAt (0 = not observed).
func onPane(name string, ms int64, key string, outAt int64) {
	stampUp = ms
	env := []string{"X=1"}
	if key != "" {
		env = append(env, opencodeKeyEnv+"="+key)
	}
	st, at := oauthState{}, time.Time{}
	if outAt > 0 {
		st, at = oauthState{known: true}, atMs(outAt)
	}
	recordSessionProcess(atMs(ms), name, procPane, env, st, at)
}

func bindFor(name string) bindFn {
	return ledgerBinder(session.Meta{Name: name, Kind: session.KindOpencode})
}

func zen(b bindFn, paid bool, ts ...int64) string {
	if b == nil {
		return "<nil binder>"
	}
	return b(routes.RouteOpencodeZen, paid, ts...)
}

// A pane's calls are bound by the key of its own launch environment, whatever the shared daemon
// held, and a replaced key on a later pane launch does not relabel the earlier pane's calls.
func TestPaneBindsByItsLaunchEnvironment(t *testing.T) {
	bindT(t)
	start(1000, 1, "daemon-key")
	acct(1100, acctOut, 1)
	onPane("t", 2000, "pane-A", 1500)
	onPane("t", 6000, "pane-B", 5500)
	b := bindFor("t")
	if got, want := zen(b, true, 3000), credbind.Key(opencodeKeyEnv, "pane-A"); got != want {
		t.Errorf("pane A call = %q, want %q", got, want)
	}
	if got, want := zen(b, true, 7000), credbind.Key(opencodeKeyEnv, "pane-B"); got != want {
		t.Errorf("pane B call = %q, want %q", got, want)
	}
	if got := zen(b, true, 3000, 7000); got != "" {
		t.Errorf("a run across two panes = %q, want none", got)
	}
	if got := zen(b, true, 1500); got != "" {
		t.Errorf("a call before any launch = %q, want none", got)
	}
}

// A pane whose handed environment has no key may still inherit one from the tmux server, so its
// calls are not bound as free.
func TestPaneKeylessIsNotProvenFree(t *testing.T) {
	bindT(t)
	onPane("t", 2000, "", 1500)
	if got := zen(bindFor("t"), false, 3000); got != "" {
		t.Errorf("keyless pane call = %q, want none", got)
	}
}

// An account not observed out at launch, or touched after the observation or after the launch, may
// have paid: no binding.
func TestPaneNeedsAccountOut(t *testing.T) {
	bindT(t)
	onPane("a", 2000, "k", 0)
	if got := zen(bindFor("a"), true, 3000); got != "" {
		t.Errorf("account never observed = %q, want none", got)
	}
	bindT(t)
	stampUp = 1800
	noteSessionAccount(atMs(1800)) // a touch between the observation and the launch
	onPane("a", 2000, "k", 1500)
	if got := zen(bindFor("a"), true, 3000); got != "" {
		t.Errorf("touched after the observation = %q, want none", got)
	}
	bindT(t)
	onPane("a", 2000, "k", 1500)
	if zen(bindFor("a"), true, 2500) == "" {
		t.Fatal("a call before the touch stays bound")
	}
	stampUp = 3000
	noteSessionAccount(atMs(3000))
	b := bindFor("a")
	if got := zen(b, true, 2500); got == "" {
		t.Errorf("a call before the touch lost its binding")
	}
	if got := zen(b, true, 3500); got != "" {
		t.Errorf("a call after the touch = %q, want none", got)
	}
	// An account event of the shared daemon's flows reaches the panes the same way.
	bindT(t)
	start(1000, 1, "d")
	onPane("a", 2000, "k", 1500)
	acct(4000, acctTouch, 1)
	if got := zen(bindFor("a"), true, 4500); got != "" {
		t.Errorf("after noteAccount touch = %q, want none", got)
	}
}

// A session that moved from a pane to Managed: the pane's calls never take the daemon's evidence,
// and the Managed calls never take the pane's key.
func TestTerminalToManagedSwitchKeepsEvidenceApart(t *testing.T) {
	bindT(t)
	start(1000, 1, "daemon-key")
	acct(1100, acctOut, 1)
	onPane("s", 2000, "pane-key", 1500)
	onShared("s", 5000) // the switch
	b := bindFor("s")
	if got, want := zen(b, true, 3000), credbind.Key(opencodeKeyEnv, "pane-key"); got != want {
		t.Errorf("pane-era call = %q, want the pane's key %q", got, want)
	}
	if got, want := zen(b, true, 6000), credbind.Key(opencodeKeyEnv, "daemon-key"); got != want {
		t.Errorf("managed-era call = %q, want the daemon's key %q", got, want)
	}
	// With the pane's account observation missing, the pane-era call is none, not the daemon's key.
	bindT(t)
	start(1000, 1, "daemon-key")
	acct(1100, acctOut, 1)
	onPane("s", 2000, "pane-key", 0)
	onShared("s", 5000)
	if got := zen(bindFor("s"), true, 3000); got != "" {
		t.Errorf("pane-era call = %q, want none", got)
	}
	if got := zen(bindFor("s"), true, 4900, 5100); got != "" {
		t.Errorf("a call across the switch = %q, want none", got)
	}
}

// A pane attached to the shared daemon is served by the daemon: its calls follow the daemon's
// evidence, and resuming the Managed runtime again does not add a boundary.
func TestSharedPeriodIsRecordedOnce(t *testing.T) {
	bindT(t)
	start(1000, 1, "K")
	acct(1100, acctOut, 1)
	onShared("s", 2000)
	onShared("s", 3000)
	if n := len(func() []sessEvent { e, _ := readSessEvents(); return e }()); n != 1 {
		t.Errorf("events = %d, want 1", n)
	}
	if got, want := zen(bindFor("s"), true, 4000), credbind.Key(opencodeKeyEnv, "K"); got != want {
		t.Errorf("shared call = %q, want %q", got, want)
	}
}

// A route server's calls are never bound from opencode.ai evidence, and a failed write leaves a
// gap that keeps the earlier launch from covering what came after it.
func TestSessionEvidenceGapsAndRoutes(t *testing.T) {
	bindT(t)
	start(1000, 1, "K")
	acct(1100, acctOut, 1)
	recordSessionProcess(atMs(2000), "r", procRoute, nil, oauthState{}, time.Time{})
	if got := zen(bindFor("r"), true, 3000); got != "" {
		t.Errorf("route server call = %q, want none", got)
	}
	onShared("s", 2000)
	noteSessGap(2500) // a launch at 2500 could not be written
	if got := zen(bindFor("s"), true, 3000); got != "" {
		t.Errorf("a call after a lost launch = %q, want none", got)
	}
	onShared("s2", 4000)
	if zen(bindFor("s2"), true, 5000) == "" {
		t.Error("a launch written after the gap is proven")
	}
}

// A wall clock set back between launches leaves the later launch's calls unproven where an earlier
// process could have made them.
func TestSessionClockSetBack(t *testing.T) {
	bindT(t)
	onPane("t", 5000, "k1", 4500)
	stampUp = 5100 // the clock then went back 3 s while 100 ms of uptime passed
	recordSessionProcess(atMs(2000), "t", procPane, []string{opencodeKeyEnv + "=k2"}, oauthState{known: true}, atMs(1500))
	if got := zen(bindFor("t"), true, 2500); got != "" {
		t.Errorf("a call at a time the earlier clock also showed = %q, want none", got)
	}
}

// Two touches before a launch: the second one, after the account observation, still counts (no
// touch is hidden behind an earlier one).
func TestPaneSeesEveryTouch(t *testing.T) {
	bindT(t)
	stampUp = 1000
	noteSessionAccount(atMs(1000))
	stampUp = 1800
	noteSessionAccount(atMs(1800)) // the credential save, after the observation at 1500
	onPane("a", 2000, "k", 1500)
	if got := zen(bindFor("a"), true, 3000); got != "" {
		t.Errorf("account saved after the observation = %q, want none", got)
	}
}

// A write lost between the account observation and the launch may have been the touch.
func TestPaneGapSinceObservation(t *testing.T) {
	bindT(t)
	_ = os.MkdirAll(paths.AgentConfigDir(), 0o700)
	noteSessGap(1800)
	onPane("a", 2000, "k", 1500)
	if got := zen(bindFor("a"), true, 3000); got != "" {
		t.Errorf("gap after the observation = %q, want none", got)
	}
}

// shared -> pane -> clock set back -> another session's launch stamped earlier: the pane's call
// is not placed on the old shared row.
func TestSessionClockChangeOfAnotherSession(t *testing.T) {
	bindT(t)
	start(500, 1, "K")
	acct(600, acctOut, 1)
	onShared("s", 1000)
	onPane("s", 7000, "k", 6500)
	stampUp = 7100
	recordSessionProcess(atMs(4000), "other", procShared, nil, oauthState{}, time.Time{})
	if got := zen(bindFor("s"), true, 5000); got != "" {
		t.Errorf("call at a time the earlier clock also showed = %q, want none", got)
	}
}

// A shared launch after a lost row is a boundary even though the newest recorded row is shared.
func TestSharedResumeAfterGapIsABoundary(t *testing.T) {
	bindT(t)
	onShared("s", 1000)
	noteSessGap(2000) // a pane launch at 2000 was lost
	onShared("s", 3000)
	if n := len(func() []sessEvent { e, _ := readSessEvents(); return e }()); n != 2 {
		t.Errorf("events = %d, want 2", n)
	}
}

// The trim keeps a running pane's only row, and what it dropped is carried as a floor.
func TestSessionTrimKeepsNewestRowPerSession(t *testing.T) {
	bindT(t)
	base := time.Now().UnixMilli() - 3_600_000 // within the month a row stays worth keeping
	onPane("old", base, "k", base-500)
	for i := 0; i < bindSessionsKeep+10; i++ {
		onShared("s", base+2000+int64(i*10))
		recordSessionProcess(atMs(base+2005+int64(i*10)), "s", procRoute, nil, oauthState{}, time.Time{})
	}
	evs, _ := readSessEvents()
	found := false
	for _, e := range evs {
		found = found || e.Session == "old"
	}
	if !found {
		t.Fatal("the trim dropped a session's newest row")
	}
	if len(evs) > bindSessionsKeep+1 {
		t.Errorf("file holds %d rows", len(evs))
	}
	// No account touch was dropped, so the kept pane row still proves what it did.
	if got := zen(bindFor("old"), true, base+1500); got == "" {
		t.Errorf("a kept pane row lost its binding to a trim that dropped no touch")
	}
}

// A flow started before the observation may complete on its own after it (the user approves on the
// Console while the Agent polls nothing): an observation shortly after a touch proves nothing, one
// long after it does.
func TestPaneObservationNeedsQuietSinceTouch(t *testing.T) {
	bindT(t)
	hold := oauthFlowHold.Milliseconds()
	stampUp = 1000
	noteSessionAccount(atMs(1000))
	onPane("a", 2000+hold, "k", 1500)
	if got := zen(bindFor("a"), true, 3000+hold); got != "" {
		t.Errorf("observed soon after a flow started = %q, want none", got)
	}
	onPane("b", 2000+2*hold, "k", 1500+2*hold)
	if got := zen(bindFor("b"), true, 3000+2*hold); got == "" {
		t.Error("an observation long after the last touch proves the account out")
	}
}

// Two unrelated launches in the same millisecond are not a clock change: earlier calls of other
// sessions stay bound.
func TestSessionSameMillisecondIsNotAClockChange(t *testing.T) {
	bindT(t)
	start(500, 1, "K")
	acct(600, acctOut, 1)
	onShared("s", 1000)
	onShared("x", 5000)
	onShared("y", 5000)
	if got, want := zen(bindFor("s"), true, 2000), credbind.Key(opencodeKeyEnv, "K"); got != want {
		t.Errorf("call = %q, want %q", got, want)
	}
}

// The watermark follows appended rows, so a gap taken after the clock went back still reaches them.
func TestSessionGapReachesAppendedRows(t *testing.T) {
	bindT(t)
	onShared("s", 7000)
	if got := sessSeenMax.Load(); got < 7000 {
		t.Fatalf("watermark = %d after appending a row at 7000", got)
	}
	sessSeenMax.Store(0) // a restart: nothing known, and the file unreadable
	if err := os.WriteFile(bindSessionsPath(), []byte("{bad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	onShared("t", 3000)
	gaps, _ := readSessGaps()
	if len(gaps) == 0 || gaps[len(gaps)-1] < 7000 {
		t.Errorf("gaps = %v, want a bound reaching every earlier row", gaps)
	}
}

// A shared restart of one session stamped before another session's row is a clock anomaly even
// though its own newest row has the same kind: it is written, not dropped as a duplicate.
func TestSharedDuplicateStillChecksTheClock(t *testing.T) {
	bindT(t)
	start(500, 1, "K")
	acct(600, acctOut, 1)
	onShared("x", 1000)
	onShared("s", 2000)
	onPane("s", 7000, "k", 6500)
	stampUp = 7100
	recordSessionProcess(atMs(4000), "x", procShared, nil, oauthState{}, time.Time{})
	if got := zen(bindFor("s"), true, 5000); got != "" {
		t.Errorf("call = %q, want none", got)
	}
}

// A call just after a launch row may belong to the previous process if the clock was set back by a
// second: it is not placed.
func TestCallJustAfterLaunchIsNotPlaced(t *testing.T) {
	bindT(t)
	callSlack = clockSlack
	onPane("a", 4000, "k", 3500)
	b := bindFor("a")
	if got := zen(b, true, 4500); got != "" {
		t.Errorf("call 0.5 s after the launch = %q, want none", got)
	}
	if got := zen(b, true, 4000+clockSlack+1); got == "" {
		t.Error("a call well after the launch stays bound")
	}
}

// A clock set back since the last row was written is not in the file yet; the fold notices it.
func TestFoldNoticesClockSetBack(t *testing.T) {
	bindT(t)
	start(500, 1, "K")
	acct(600, acctOut, 1)
	onShared("s", 1000)
	b := bindFor("s")
	if zen(b, true, 3000) == "" {
		t.Fatal("setup: shared call must bind")
	}
	stampUp = 1100 // 100 ms of uptime later the wall clock reads 20 s earlier
	wallNow = func() time.Time { return time.UnixMilli(-19000) }
	if got := zen(bindFor("s"), true, 3000); got != "" {
		t.Errorf("call after the clock was set back = %q, want none", got)
	}
}

// A signed-out observation soon after a lost write is not proof either.
func TestPaneGapInsideQuietPeriod(t *testing.T) {
	bindT(t)
	_ = os.MkdirAll(paths.AgentConfigDir(), 0o700)
	hold := oauthFlowHold.Milliseconds()
	noteSessGap(1000)
	onPane("a", 2000+hold, "k", 1500+hold/2)
	if got := zen(bindFor("a"), true, 5000+hold); got != "" {
		t.Errorf("observed soon after a lost write = %q, want none", got)
	}
}
