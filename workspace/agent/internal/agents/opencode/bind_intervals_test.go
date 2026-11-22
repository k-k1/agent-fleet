package opencode

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// stampUp is the uptime the test clock reports for the next event; a test that wants a consistent
// clock sets it equal to the event's wall time.
var stampUp int64

func bindT(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	credbind.ResetForTest()
	old := clockStamp
	clockStamp = func() (string, int64, bool) { return "boot", stampUp, true }
	oldNow, oldSlack, oldAge := wallNow, callSlack, instantAge
	instantAge = func(_, _ time.Time) (time.Duration, time.Duration) { return 0, 0 } // test instants are taken as now
	callSlack = 0
	wallNow = func() time.Time { return time.UnixMilli(stampUp) }
	gapsMem, sessGapsMem = nil, nil
	seenMaxAt.Store(0)
	bindOff.Store(false)
	sessOff.Store(false)
	t.Cleanup(func() {
		credbind.ResetForTest()
		clockStamp = old
		wallNow, callSlack, instantAge = oldNow, oldSlack, oldAge
		gapsMem, sessGapsMem = nil, nil
	})
}

// start and acct write events whose uptime follows their wall time (a clock that was not set).
func start(ms int64, gen int, key string) {
	stampUp = ms
	env := []string{"X=1"}
	if key != "" {
		env = append(env, opencodeKeyEnv+"="+key)
	}
	noteDaemonStart(spawnStamp{at: atMs(ms), boot: "boot", up: ms, stamped: true}, env, gen)
}

func acct(ms int64, a string, gen int) {
	stampUp = ms
	noteAccount(atMs(ms), a, gen)
}

func events(t *testing.T) []bindEvent {
	t.Helper()
	evs, ok := readBindEvents()
	if !ok {
		t.Fatal("evidence unreadable")
	}
	return evs
}

func bind(t *testing.T, route string, paid bool, ts ...int64) string {
	t.Helper()
	gaps, ok := readGaps()
	if !ok {
		t.Fatal("gaps unreadable")
	}
	return bindingAt(events(t), gaps, route, paid, ts...)
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
	start(1000, 1, "key-A")
	acct(1500, acctOut, 1)
	start(5000, 2, "key-B")
	acct(5500, acctOut, 2)
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
	start(1000, 1, "")
	acct(1100, acctOut, 1)
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
	acct(3000, acctTouch, 1)
	acct(3500, acctOut, 1)
	if got := bind(t, routes.RouteOpencodeZen, false, 2000); got != FreeBinding {
		t.Errorf("a call before the touch stays bound: %q", got)
	}
	if got := bind(t, routes.RouteOpencodeZen, false, 4000); got != "" {
		t.Errorf("a call after the touch = %q, want none", got)
	}
}

// An adopted daemon proves nothing.
func TestBindingAdoptedDaemonProvesNothing(t *testing.T) {
	bindT(t)
	start(1000, 1, "A")
	acct(1100, acctOut, 1)
	appendBindEvent(bindEvent{At: 6000, Boot: "boot", Up: 6000, Kind: evUnknown, Gen: 2}, nil)
	if got := bind(t, routes.RouteOpencodeZen, false, 2000); got == "" {
		t.Error("a call before the adoption stays bound")
	}
	if got := bind(t, routes.RouteOpencodeZen, false, 7000); got != "" {
		t.Errorf("adopted daemon = %q, want none", got)
	}
}

// A reading counts only for the generation it came from, and only while that generation is the
// newest: a reading from a replaced daemon never lands in its successor's life.
func TestBindingReadingsBelongToTheirGeneration(t *testing.T) {
	bindT(t)
	start(1000, 1, "A")
	start(2000, 2, "B")
	acct(2100, acctOut, 1) // read from generation 1, arriving after generation 2 started
	if got := bind(t, routes.RouteOpencodeZen, false, 3000); got != "" {
		t.Errorf("a stale reading made B's life bound: %q", got)
	}
	acct(2200, acctOut, 2)
	if got := bind(t, routes.RouteOpencodeZen, false, 3000); got == "" {
		t.Error("the right generation's reading binds")
	}
}

// A later reading never changes what an earlier call binds to, so a ledger retry folds the same
// row it folded first.
func TestBindingStableAsEvidenceGrows(t *testing.T) {
	bindT(t)
	start(1000, 1, "k")
	acct(1100, acctOut, 1)
	first := bind(t, routes.RouteOpencodeZen, false, 2000)
	acct(3000, acctIn, 1)
	start(9000, 2, "k2")
	if got := bind(t, routes.RouteOpencodeZen, false, 2000); got != first || first == "" {
		t.Errorf("binding moved from %q to %q", first, got)
	}
}

// A clock that was set (wall time and uptime disagree between two events), a start that shares the
// call's millisecond, and a run whose steps fall in different daemons prove nothing. The doubt
// outlives the daemon that showed it: a later daemon whose calls overlap the old clock's range is
// unbound too.
func TestBindingRefusesAmbiguousIntervals(t *testing.T) {
	bindT(t)
	start(10000, 1, "A")
	acct(10100, acctOut, 1)
	// A runs and makes calls stamped up to 31000 on the wall; then the clock is set back, so B is
	// stamped 14000 on the wall while the uptime says 32000.
	noteDaemonStart(spawnStamp{at: atMs(14000), boot: "boot", up: 32000, stamped: true}, []string{opencodeKeyEnv + "=B"}, 2)
	stampUp = 32100
	noteAccount(atMs(14100), acctOut, 2)
	// C starts 6 s later on both clocks: nothing looks wrong about C itself.
	stampUp = 38000
	noteDaemonStart(spawnStamp{at: atMs(20000), boot: "boot", up: 38000, stamped: true}, []string{opencodeKeyEnv + "=C"}, 3)
	stampUp = 38100
	noteAccount(atMs(21000), acctOut, 3)
	for _, ts := range []int64{14500, 30000, 31000} { // A's calls read as B's or C's by time
		if got := bind(t, routes.RouteOpencodeZen, false, ts); got != "" {
			t.Errorf("at %d after the clock was set = %q, want none", ts, got)
		}
	}
	if got := bind(t, routes.RouteOpencodeZen, false, 40000); got == "" {
		t.Error("a call beyond everything the old clock could show binds again")
	}
	bindT(t)
	start(1000, 1, "A")
	acct(1100, acctOut, 1)
	start(5000, 2, "A") // same key, restarted
	acct(5100, acctOut, 2)
	if got := bind(t, routes.RouteOpencodeZen, false, 2000, 3000); got == "" {
		t.Error("two steps of one daemon must bind")
	}
	if got := bind(t, routes.RouteOpencodeZen, false, 2000, 6000); got != "" {
		t.Errorf("a run across a restart = %q, want none even with the same key", got)
	}
}

// A daemon that merely takes a few seconds to answer is not a clock change: the wall time and the
// uptime of a start are taken together at spawn.
func TestBindingSlowStartIsNotAClockChange(t *testing.T) {
	bindT(t)
	start(1000, 1, "A")
	acct(1100, acctOut, 1)
	stampUp = 9000 // the writer runs 3 s after the spawn stamp below
	noteDaemonStart(spawnStamp{at: atMs(6000), boot: "boot", up: 6000, stamped: true}, []string{opencodeKeyEnv + "=B"}, 2)
	stampUp = 9100
	noteAccount(atMs(9100), acctOut, 2)
	if got := bind(t, routes.RouteOpencodeZen, false, 10000); got == "" {
		t.Error("a slow start must stay bound")
	}
}

// A damaged file proves nothing; so does a boundary that could not be written, and that stays so
// after a later daemon event succeeds and after a restart.
func TestBindingNeedsIntactEvidence(t *testing.T) {
	bindT(t)
	start(1000, 1, "A")
	acct(1100, acctOut, 1)
	m := session.Meta{Name: "s", Kind: session.KindOpencode, Driver: session.DriverManaged}
	onShared("s", 500)
	if zen(ledgerBinder(m), true, 2000) == "" {
		t.Fatal("intact evidence binds")
	}
	f, err := os.OpenFile(bindIntervalsPath(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("{not json\n")
	f.Close()
	if got := zen(ledgerBinder(m), true, 2000); got != "" {
		t.Errorf("a damaged line must disable binding, got %q", got)
	}

	bindT(t)
	start(1000, 1, "A")
	acct(1100, acctOut, 1)
	// The touch at 3000 cannot be written (the file is a directory now).
	p := bindIntervalsPath()
	good, _ := os.ReadFile(p)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	acct(3000, acctTouch, 1) // unreadable evidence: a gap
	_ = os.Remove(p)
	if err := os.WriteFile(p, good, 0o600); err != nil {
		t.Fatal(err)
	}
	start(9000, 2, "A") // a later success must not undo the gap
	acct(9100, acctOut, 2)
	if got := bind(t, routes.RouteOpencodeZen, false, 4000); got != "" {
		t.Errorf("a call of a life whose boundary was lost = %q, want none", got)
	}
	if got := bind(t, routes.RouteOpencodeZen, false, 10000); got == "" {
		t.Error("a life that began after the gap stays bound")
	}
	// After a restart the gaps file still says so.
	gapsMem = nil
	if got := bind(t, routes.RouteOpencodeZen, false, 4000); got != "" {
		t.Errorf("after a restart = %q, want none", got)
	}
	if _, err := os.Stat(filepath.Join(paths.AgentConfigDir(), bindGapsFile)); err != nil {
		t.Errorf("the gap was not persisted: %v", err)
	}
}

// The file stays bounded, and the newest daemon event survives the trim with its readings.
func TestBindingTrimKeepsLiveDaemon(t *testing.T) {
	bindT(t)
	for i := 0; i < 2*bindIntervalsKeep; i++ {
		start(1000+int64(i)*10, i+1, "A")
		acct(1005+int64(i)*10, acctOut, i+1)
	}
	if got := bind(t, routes.RouteOpencodeZen, false, 900000); got == "" {
		t.Error("the live daemon lost its evidence to the trim")
	}
	if n := len(events(t)); n > bindIntervalsKeep+1 {
		t.Errorf("file holds %d events, bound is %d", n, bindIntervalsKeep)
	}
}

// The daemon that answered is the process that holds the listening socket: this test process owns a
// listener it opened, and nobody else does.
func TestOwnsListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback listener")
	}
	defer ln.Close()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	if !ownsListener(os.Getpid(), port) {
		t.Error("the process that listens owns the port")
	}
	if ownsListener(os.Getppid(), port) {
		t.Error("another process must not own it")
	}
	if ownsListener(os.Getpid(), "1") {
		t.Error("a port nothing listens on is owned by nobody")
	}
}

func connOf(b bindFn, msgs []v2Msg, born int64) []string {
	var got []string
	for _, tn := range v2SessionTurnsBound(msgs, "/w", born, false, true, nil, b) {
		if tn.turn.Role == "assistant" {
			got = append(got, tn.turn.Connection)
		}
	}
	return got
}

// The folded ledger turn carries the binding of its call's interval; a forked copy, a run across
// two daemons, a step with no end, a paid keyless call and sessions off the shared daemon carry none.
func TestV2TurnsBoundByInterval(t *testing.T) {
	bindT(t)
	keyA := credbind.Key(opencodeKeyEnv, "key-A")
	start(1000, 1, "key-A")
	acct(1100, acctOut, 1)
	start(8000, 2, "key-B")
	acct(8100, acctOut, 2)
	m := session.Meta{Name: "s", Kind: session.KindOpencode, Driver: session.DriverManaged}
	onShared("s", 500)
	b := ledgerBinder(m)
	if b == nil {
		t.Fatal("a managed session on the shared daemon has a binder")
	}
	user := v2Msg{Type: "user"}
	user.Time.Created = 1500
	step := routeMsg("opencode", true, 0.5) // created 2000, completed 3000
	if got := connOf(b, []v2Msg{user, step}, 0); len(got) != 1 || got[0] != keyA {
		t.Errorf("connection = %q, want %q", got, keyA)
	}
	if got := connOf(b, []v2Msg{user, step}, 2500); len(got) != 1 || got[0] != "" {
		t.Errorf("a copied message = %q, want none", got)
	}
	straddle := routeMsg("opencode", true, 0.5)
	straddle.Time.Completed = 9000
	if got := connOf(b, []v2Msg{user, straddle}, 0); len(got) != 1 || got[0] != "" {
		t.Errorf("a call spanning two daemons = %q, want none", got)
	}
	open := routeMsg("opencode", true, 0.5)
	open.Time.Completed = 0
	if got := connOf(b, []v2Msg{user, open}, 0); len(got) != 1 || got[0] != "" {
		t.Errorf("a step with no end = %q, want none", got)
	}
	// A -> B -> A: both ends agree, a step in the middle does not.
	start(12000, 3, "key-A")
	acct(12100, acctOut, 3)
	s1, s2, s3 := routeMsg("opencode", true, 0.5), routeMsg("opencode", true, 0.5), routeMsg("opencode", true, 0.5)
	s1.Time.Created, s1.Time.Completed = 2000, 2500
	s2.Time.Created, s2.Time.Completed = 9000, 9500
	s3.Time.Created, s3.Time.Completed = 13000, 13500
	b = ledgerBinder(m)
	if got := connOf(b, []v2Msg{user, s1, s2, s3}, 0); len(got) != 1 || got[0] != "" {
		t.Errorf("a run A,B,A = %q, want none", got)
	}
	for name, meta := range map[string]session.Meta{
		"own route server": {Name: "r", Kind: session.KindOpencode, Driver: session.DriverManaged, Route: &routes.Selection{Route: routes.RouteBedrock}},
		"no launch record": {Name: "u", Kind: session.KindOpencode, Driver: session.DriverManaged},
	} {
		if meta.Name == "r" {
			recordSessionProcess(atMs(500), "r", procRoute, nil, oauthState{}, time.Time{})
		}
		if b := ledgerBinder(meta); b != nil && connOf(b, []v2Msg{user, step}, 0)[0] != "" {
			t.Errorf("%s: must not be bound from the shared daemon's evidence", name)
		}
	}
}

func atMs(ms int64) time.Time { return time.UnixMilli(ms) }

// A touch recorded while the clock was ahead is still a touch after the clock comes back.
func TestBindingTouchSurvivesClockSetBack(t *testing.T) {
	bindT(t)
	start(1000, 1, "A")
	acct(2000, acctOut, 1)
	// The wall clock is 10 s ahead of the uptime when the sign-in flow touches the account.
	stampUp = 3000
	noteAccount(atMs(13000), acctTouch, 1)
	if got := bind(t, routes.RouteOpencodeZen, false, 6000); got != "" {
		t.Errorf("a call after a touch whose clock was ahead = %q, want none", got)
	}
}

// The floor of a Reg event survives the trim that drops the event.
func TestBindingTrimCarriesTheClockFloor(t *testing.T) {
	bindT(t)
	start(10000, 1, "A")
	acct(10100, acctOut, 1)
	// B stamped 14000 on the wall, 32000 on the uptime: A's late calls overlap B's and C's range.
	noteDaemonStart(spawnStamp{at: atMs(14000), boot: "boot", up: 32000, stamped: true}, []string{opencodeKeyEnv + "=B"}, 2)
	for i := 0; i < bindIntervalsKeep+10; i++ { // enough later lives that the trim drops B
		up := int64(38000 + 10*i)
		noteDaemonStart(spawnStamp{at: atMs(20000 + 10*int64(i)), boot: "boot", up: up, stamped: true}, []string{opencodeKeyEnv + "=C"}, 3+i)
		stampUp = up + 5
		noteAccount(atMs(20000+10*int64(i)+5), acctOut, 3+i)
	}
	if got := bind(t, routes.RouteOpencodeZen, false, 30000); got != "" {
		t.Errorf("an old call that overlaps the new lives = %q, want none after the trim", got)
	}
}

// A gap recorded while the file cannot be read reaches every life known before, not only the
// failed event's own time.
func TestBindingGapWhileUnreadableCoversKnownLives(t *testing.T) {
	bindT(t)
	start(100000, 1, "A")
	acct(101000, acctOut, 1)
	if _, ok := readBindEvents(); !ok {
		t.Fatal("setup")
	}
	p := bindIntervalsPath()
	good, _ := os.ReadFile(p)
	_ = os.Remove(p)
	_ = os.Mkdir(p, 0o700)
	acct(90000, acctTouch, 1) // clock set back; the file is unreadable now
	_ = os.Remove(p)
	_ = os.WriteFile(p, good, 0o600)
	if got := bind(t, routes.RouteOpencodeZen, false, 102000); got != "" {
		t.Errorf("a known life after a gap taken while unreadable = %q, want none", got)
	}
}

// A restart whose first read of the evidence file fails, on a wall clock set back behind an older
// life: the older life stays unproven once the file is readable again, though the gap itself was
// recorded with the set-back clock. The state survives a second restart.
func TestBindingUnknownGapRetiresOlderLivesAfterClockSetBack(t *testing.T) {
	bindT(t)
	start(5000, 1, "A")
	acct(5100, acctOut, 1)
	// Agent restart: nothing is known in this process, the file cannot be read, the clock is at 600.
	gapsMem = nil
	seenMaxAt.Store(0)
	p := bindIntervalsPath()
	good, _ := os.ReadFile(p)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	start(600, 2, "B") // not recorded: a gap bounded by the set-back clock
	_ = os.Remove(p)
	if err := os.WriteFile(p, good, 0o600); err != nil {
		t.Fatal(err)
	}
	if b := daemonBinder(); b == nil {
		t.Fatal("no binder once the file is readable")
	} else if got := b(routes.RouteOpencodeZen, false, 5500); got != "" {
		t.Errorf("a call of the older life stamped after the set-back clock = %q, want none", got)
	}
	// Resolved once: a life that starts later is bound, and a restart keeps the older one retired.
	start(9000, 3, "C")
	acct(9100, acctOut, 3)
	gapsMem = nil
	b := daemonBinder()
	if b == nil {
		t.Fatal("no binder")
	}
	if got := b(routes.RouteOpencodeZen, false, 5500); got != "" {
		t.Errorf("after a restart = %q, want none", got)
	}
	if got := b(routes.RouteOpencodeZen, false, 10000); got == "" {
		t.Error("a life after the resolution is bound")
	}
}

// The first healthy read after the failure is an append (a daemon start), not a binder read: the
// older life is retired there, the life that start begins is bound, and a recovery after a clock
// correction does not retire it for good.
func TestBindingUnknownGapResolvedByAppend(t *testing.T) {
	bindT(t)
	start(5000, 1, "A")
	acct(5100, acctOut, 1)
	gapsMem = nil
	seenMaxAt.Store(0)
	p := bindIntervalsPath()
	good, _ := os.ReadFile(p)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	start(600, 2, "B")
	_ = os.Remove(p)
	if err := os.WriteFile(p, good, 0o600); err != nil {
		t.Fatal(err)
	}
	gapsMem = nil // only the gaps file carries the state
	start(9000, 3, "C")
	acct(9100, acctOut, 3)
	gapsMem = nil
	b := daemonBinder()
	if b == nil {
		t.Fatal("no binder")
	}
	if got := b(routes.RouteOpencodeZen, false, 5500); got != "" {
		t.Errorf("older life = %q, want none", got)
	}
	if got := b(routes.RouteOpencodeZen, false, 10000); got == "" {
		t.Error("the life started after the recovery is bound")
	}
}

// A gaps file that cannot be read for a moment gives no binder for that call and does not turn
// binding off.
func TestBindingGapsUnreadableIsTransient(t *testing.T) {
	bindT(t)
	start(1000, 1, "A")
	acct(1100, acctOut, 1)
	gp := filepath.Join(paths.AgentConfigDir(), bindGapsFile)
	if err := os.MkdirAll(gp, 0o700); err != nil { // a directory: the read fails
		t.Fatal(err)
	}
	if daemonBinder() != nil {
		t.Error("binder with an unreadable gaps file")
	}
	if bindOff.Load() {
		t.Error("a read failure turned binding off")
	}
	_ = os.Remove(gp)
	if daemonBinder() == nil {
		t.Error("no binder after the gaps file is readable again")
	}
}

// The retire bound is the newest daemon event's At: a clock correction recorded earlier in the file
// (a Floor above the later lives' starts) does not retire a
// life that starts after the recovery.
func TestBindingUnknownGapBoundIgnoresFloor(t *testing.T) {
	bindT(t)
	dir := paths.AgentConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := []bindEvent{
		{At: 7000, Kind: evStart, Key: "A", Gen: 1, Reg: true, Floor: 14000},
		{At: 7100, Kind: evAccount, Acct: acctOut, Gen: 1},
	}
	var sb strings.Builder
	for _, e := range old {
		b, _ := json.Marshal(e)
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(bindIntervalsPath(), []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, bindGapsFile), []byte(gapPending+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	start(8000, 2, "C")
	acct(8100, acctOut, 2)
	// The bound is the one daemon event's At, not the account reading's (7100) nor the Floor.
	if gaps, pending, ok := readGapsFile(); !ok || pending || len(gaps) != 1 || gaps[0] != 7000 {
		t.Errorf("gaps file = %v pending=%v ok=%v, want [7000] resolved", gaps, pending, ok)
	}
	if got := bind(t, routes.RouteOpencodeZen, false, 7500); got != "" {
		t.Errorf("older life = %q, want none", got)
	}
	if got := bind(t, routes.RouteOpencodeZen, false, 15000); got == "" {
		t.Error("the life started after the recovery is bound")
	}
}

// holdLock takes the lock the way a second Agent process would (its own open file description) and
// returns the release.
func holdLock(t *testing.T) func() {
	t.Helper()
	if err := os.MkdirAll(paths.AgentConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(paths.AgentConfigDir(), bindLockFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }
}

// waitRetry runs fn in a goroutine and returns once it has failed to get the lock at least once,
// so "still blocked" is checked after the call really reached the lock.
func waitRetry(t *testing.T, fn func()) (done chan struct{}) {
	t.Helper()
	tried := make(chan struct{}, 1)
	bindLockRetry = func() {
		select {
		case tried <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() { bindLockRetry = nil })
	done = make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-tried:
	case <-time.After(5 * time.Second):
		t.Fatal("call never reached the lock")
	}
	return done
}

func TestBindingAppendWaitsForCrossProcessLock(t *testing.T) {
	bindT(t)
	unlock := holdLock(t)
	done := waitRetry(t, func() { start(5000, 1, "A") })
	select {
	case <-done:
		t.Fatal("append ran while another process held the lock")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("append did not proceed after the lock was released")
	}
	if evs, ok := readBindEvents(); !ok || len(evs) != 1 {
		t.Fatalf("events = %v ok=%v, want one", evs, ok)
	}
}

// daemonBinder resolves a pending gap marker only under the lock.
func TestBindingDaemonBinderWaitsForCrossProcessLock(t *testing.T) {
	bindT(t)
	start(5000, 1, "A")
	acct(5100, acctOut, 1)
	gp := filepath.Join(paths.AgentConfigDir(), bindGapsFile)
	if err := os.WriteFile(gp, []byte(gapPending+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unlock := holdLock(t)
	done := waitRetry(t, func() { daemonBinder() })
	select {
	case <-done:
		t.Fatal("daemonBinder ran while another process held the lock")
	case <-time.After(100 * time.Millisecond):
	}
	if _, pending, ok := readGapsFile(); !ok || !pending {
		t.Errorf("pending marker resolved while the lock was held (pending=%v ok=%v)", pending, ok)
	}
	unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("daemonBinder did not proceed after the lock was released")
	}
	if _, pending, ok := readGapsFile(); !ok || pending {
		t.Errorf("marker not resolved after release (pending=%v ok=%v)", pending, ok)
	}
}

// A holder that never lets go costs a bounded wait, then both paths run unlocked.
func TestBindingLockWaitIsBounded(t *testing.T) {
	bindT(t)
	old := bindLockWait
	bindLockWait = 50 * time.Millisecond
	t.Cleanup(func() { bindLockWait = old })
	unlock := holdLock(t)
	defer unlock()
	start(5000, 1, "A")
	acct(5100, acctOut, 1)
	if daemonBinder() == nil {
		t.Error("no binder while the lock is held past the wait")
	}
}

// A lock file that cannot be opened does not stop either path.
func TestBindingLockOpenFailureContinues(t *testing.T) {
	bindT(t)
	if err := os.MkdirAll(filepath.Join(paths.AgentConfigDir(), bindLockFile), 0o700); err != nil {
		t.Fatal(err)
	}
	start(5000, 1, "A")
	acct(5100, acctOut, 1)
	if daemonBinder() == nil {
		t.Error("no binder with an unopenable lock file")
	}
}
