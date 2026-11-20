package sessionx

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// bgWakeFixture runs the real tick / observe / verdict / send path over one fake agy session:
// the meta is a real file in a temp dir, the live readings are fields, delivery is recorded.
type bgWakeFixture struct {
	t        *testing.T
	name     string
	alive    bool
	state    string
	busy     bool
	turnEnd  string
	mark     string
	markOK   bool
	ready    bool
	queued   bool
	fail     error
	sent     []string
	attempts []time.Duration // offsets from start of every delivery attempt
	at       time.Duration   // the offset of the tick being run
	reads    int
	start    time.Time
}

func newBgWakeFixture(t *testing.T) *bgWakeFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	f := &bgWakeFixture{t: t, name: "agy1", alive: true, state: "idle", turnEnd: "2026-11-20T01:55:25+09:00",
		mark: "conv:41", markOK: true, ready: true, start: time.Date(2026, 11, 20, 1, 55, 30, 0, time.UTC)}
	session.WriteMeta(session.Meta{Name: f.name, Kind: session.KindAgy, Dir: t.TempDir()})
	oa, ow, op, or, om, oq, oi, oo := backgroundWakeAlive, backgroundWakeRead, backgroundWakeProbe, backgroundWakeReady,
		backgroundWakeMark, backgroundWakeQueued, backgroundWakeInject, backgroundWakeOptedIn
	backgroundWakeAlive = func(session.Meta) bool { return f.alive }
	backgroundWakeRead = func(session.Meta) bgWakeLive {
		f.reads++
		return bgWakeLive{state: f.state, busy: f.busy, turnEnd: f.turnEnd}
	}
	backgroundWakeProbe = func(session.Meta) bool { return f.busy }
	backgroundWakeReady = func(session.Meta) bool { return f.ready }
	backgroundWakeMark = func(session.Meta) (string, bool) { return f.mark, f.markOK }
	backgroundWakeQueued = func(string) bool { return f.queued }
	backgroundWakeInject = func(name, prompt string, _ agents.Origin) error {
		f.attempts = append(f.attempts, f.at)
		if f.fail != nil {
			return f.fail
		}
		f.sent = append(f.sent, prompt)
		return nil
	}
	backgroundWakeOptedIn = func(kind string) bool { return kind == session.KindAgy }
	bgWakeMu.Lock()
	bgWakePeriods = map[string]*bgWakePeriod{}
	bgWakeMu.Unlock()
	t.Cleanup(func() {
		backgroundWakeAlive, backgroundWakeRead, backgroundWakeProbe, backgroundWakeReady,
			backgroundWakeMark, backgroundWakeQueued, backgroundWakeInject, backgroundWakeOptedIn = oa, ow, op, or, om, oq, oi, oo
	})
	return f
}

func (f *bgWakeFixture) tick(d time.Duration) {
	f.at = d
	backgroundWakeTick(f.start.Add(d))
}

func secs(n int) time.Duration { return time.Duration(n) * time.Second }

// busyThenQuiet runs a background period from 0 to 30 s, then quiet ticks to 30+quiet s.
func (f *bgWakeFixture) busyThenQuiet(quiet int) {
	f.busy = true
	f.tick(0)
	f.tick(secs(15))
	f.busy = false
	for s := 30; s <= 30+quiet; s += 15 {
		f.tick(secs(s))
	}
}

func (f *bgWakeFixture) restartAgent() {
	bgWakeMu.Lock()
	bgWakePeriods = map[string]*bgWakePeriod{}
	bgWakeMu.Unlock()
}

func TestBackgroundWake_NudgesOnceWhenNoTurnFollows(t *testing.T) {
	f := newBgWakeFixture(t)
	f.busyThenQuiet(45)
	if len(f.sent) != 0 {
		t.Fatalf("nudged before the grace was over: %q", f.sent)
	}
	f.tick(secs(90))
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "background task(s) have finished") {
		t.Fatalf("want one nudge after the grace, got %q", f.sent)
	}
	for s := 105; s <= 600; s += 15 {
		f.tick(secs(s))
	}
	if len(f.sent) != 1 {
		t.Fatalf("nudged the same period again: %d sends", len(f.sent))
	}
	m, _ := session.ReadMeta(f.name)
	if m.BackgroundNudge == nil || m.BackgroundNudge.Mark != "conv:41" || len(m.BackgroundNudge.Recent) != 1 {
		t.Fatalf("nudge not recorded on the meta: %+v", m.BackgroundNudge)
	}
}

func TestBackgroundWake_NoNudgeWhenTheCLIStartedItsOwnTurn(t *testing.T) {
	for _, tc := range []struct {
		name string
		move func(f *bgWakeFixture)
	}{
		{"turn ended, mark moved", func(f *bgWakeFixture) { f.mark = "conv:57" }},
		{"turn ended, last turn end moved", func(f *bgWakeFixture) { f.turnEnd = "2026-11-20T02:43:40+09:00" }},
		{"turn still running", func(f *bgWakeFixture) { f.state = "working" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBgWakeFixture(t)
			f.busy = true
			f.tick(0)
			f.busy = false
			tc.move(f)
			for s := 15; s <= 300; s += 15 {
				f.tick(secs(s))
			}
			if len(f.sent) != 0 {
				t.Fatalf("nudged a session whose CLI resumed by itself: %q", f.sent)
			}
		})
	}
}

func TestBackgroundWake_NeverIntoAPersonWaitOrAStoppedSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(f *bgWakeFixture)
	}{
		{"question / permission / limit menu", func(f *bgWakeFixture) { f.state = "question" }},
		{"not ready for input", func(f *bgWakeFixture) { f.ready = false }},
		{"stopped", func(f *bgWakeFixture) { f.alive = false }},
		{"archived", func(f *bgWakeFixture) {
			UpdateSessionMeta(f.name, func(m *session.Meta) bool { m.Archived = true; return true })
		}},
		{"armed to stop after its turn", func(f *bgWakeFixture) {
			UpdateSessionMeta(f.name, func(m *session.Meta) bool { m.StopAfterTurnAt = f.start.Format(time.RFC3339); return true })
		}},
		{"peer message queued", func(f *bgWakeFixture) { f.queued = true }},
		{"mark unreadable", func(f *bgWakeFixture) { f.markOK = false }},
		{"being retired", func(f *bgWakeFixture) {
			UpdateSessionMeta(f.name, func(m *session.Meta) bool { m.RetiredAt = "2026-11-20T01:56:00Z"; return true })
		}},
		{"first prompt still pending", func(f *bgWakeFixture) {
			UpdateSessionMeta(f.name, func(m *session.Meta) bool { m.InitialPromptState = session.InitialPromptPending; return true })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBgWakeFixture(t)
			f.busy = true
			f.tick(0)
			f.busy = false
			tc.set(f)
			for s := 15; s <= 300; s += 15 {
				f.tick(secs(s))
			}
			if len(f.sent) != 0 {
				t.Fatalf("nudged: %q", f.sent)
			}
		})
	}
}

func TestBackgroundWake_FlappingBusyRestartsTheGrace(t *testing.T) {
	f := newBgWakeFixture(t)
	f.busy = true
	f.tick(0)
	// Busy and quiet alternate every tick for five minutes: never 60 s of quiet in a row.
	for s := 15; s <= 300; s += 15 {
		f.busy = (s/15)%2 == 0
		f.tick(secs(s))
	}
	if len(f.sent) != 0 {
		t.Fatalf("nudged on a flapping reading: %q", f.sent)
	}
	f.busy = false
	for s := 315; s <= 390; s += 15 {
		f.tick(secs(s))
	}
	if len(f.sent) != 1 {
		t.Fatalf("want one nudge once the quiet held, got %d", len(f.sent))
	}
}

func TestBackgroundWake_RestartDoesNotNudgeTheSameTurnTwice(t *testing.T) {
	f := newBgWakeFixture(t)
	f.busyThenQuiet(60)
	if len(f.sent) != 1 {
		t.Fatalf("want the first nudge, got %d", len(f.sent))
	}
	// The model ignored it (no turn), something lit BackgroundBusy again, and the Agent
	// restarted in between: the meta still knows this finished turn was nudged.
	f.restartAgent()
	f.start = f.start.Add(10 * time.Minute)
	f.busyThenQuiet(120)
	if len(f.sent) != 1 {
		t.Fatalf("nudged the same finished turn after a restart: %d sends", len(f.sent))
	}
}

func TestBackgroundWake_HourlyCapStopsALoop(t *testing.T) {
	f := newBgWakeFixture(t)
	for i := 0; i < 5; i++ {
		// Every nudge is answered by a turn that backgrounds work again and never collects it.
		f.mark = "conv:" + string(rune('a'+i))
		f.busyThenQuiet(60)
		f.start = f.start.Add(5 * time.Minute)
	}
	if len(f.sent) != backgroundWakeMaxPerHour {
		t.Fatalf("want %d nudges in the hour, got %d", backgroundWakeMaxPerHour, len(f.sent))
	}
	f.start = f.start.Add(time.Hour)
	f.mark = "conv:z"
	f.busyThenQuiet(60)
	if len(f.sent) != backgroundWakeMaxPerHour+1 {
		t.Fatalf("the cap did not lift after the hour: %d sends", len(f.sent))
	}
}

func TestBackgroundWake_DeliveryFailureRetriesBoundedly(t *testing.T) {
	f := newBgWakeFixture(t)
	f.fail = errors.New("pane gone")
	f.busyThenQuiet(60)
	for s := 105; s <= 1800; s += 15 {
		f.tick(secs(s))
	}
	if len(f.attempts) != backgroundWakeAttempts {
		t.Fatalf("want %d attempts, got %d at %v", backgroundWakeAttempts, len(f.attempts), f.attempts)
	}
	for i := 1; i < len(f.attempts); i++ {
		if gap := f.attempts[i] - f.attempts[i-1]; gap < backgroundWakeRetry {
			t.Fatalf("attempt %d came %s after the previous one, want >= %s", i, gap, backgroundWakeRetry)
		}
	}
	f.fail = nil
	for s := 1815; s <= 2400; s += 15 {
		f.tick(secs(s))
	}
	if len(f.sent) != 0 || len(f.attempts) != backgroundWakeAttempts {
		t.Fatalf("kept trying past the bound: sent %q, %d attempts", f.sent, len(f.attempts))
	}
	if m, _ := session.ReadMeta(f.name); m.BackgroundNudge != nil {
		t.Fatalf("a failed delivery was recorded as a nudge: %+v", m.BackgroundNudge)
	}
}

func TestBackgroundWake_DeliveryRecoversWithinTheBound(t *testing.T) {
	f := newBgWakeFixture(t)
	f.fail = errors.New("pane busy")
	f.busyThenQuiet(60) // first attempt fails
	f.fail = nil
	for s := 105; s <= 400; s += 15 {
		f.tick(secs(s))
	}
	if len(f.attempts) != 2 || len(f.sent) != 1 {
		t.Fatalf("want a failed attempt then one delivery, got %d attempts, %d sent", len(f.attempts), len(f.sent))
	}
	if gap := f.attempts[1] - f.attempts[0]; gap < backgroundWakeRetry {
		t.Fatalf("retried after %s, want >= %s", gap, backgroundWakeRetry)
	}
}

func TestBackgroundWake_OnlyAgyOptsIn(t *testing.T) {
	newBgWakeFixture(t)
	backgroundWakeOptedIn = backgroundWakeOptedInDefault
	for kind, want := range map[string]bool{
		session.KindAgy: true, session.KindClaude: false, session.KindCodex: false,
		session.KindMuse: false, session.KindShell: false,
	} {
		if got := backgroundWakeOptedIn(kind); got != want {
			t.Errorf("%s: opted in = %v, want %v", kind, got, want)
		}
	}
}

// A later turn that backgrounds work again (agy resumed once by itself, then stalled on its
// second batch) starts a period of its own, keyed by its own mark.
func TestBackgroundWake_LaterTurnStartsItsOwnPeriod(t *testing.T) {
	f := newBgWakeFixture(t)
	f.busy = true
	f.tick(0)
	f.busy = false
	f.state = "working" // agy resumed by itself
	for s := 15; s <= 120; s += 15 {
		f.tick(secs(s))
	}
	f.state, f.busy, f.mark = "idle", true, "conv:57" // that turn ended and backgrounded again
	f.tick(secs(135))
	f.busy = false
	for s := 150; s <= 240; s += 15 {
		f.tick(secs(s))
	}
	if len(f.sent) != 1 {
		t.Fatalf("the second, unanswered batch was not nudged: %d sends", len(f.sent))
	}
	if m, _ := session.ReadMeta(f.name); m.BackgroundNudge == nil || m.BackgroundNudge.Mark != "conv:57" {
		t.Fatalf("nudge recorded against the wrong turn: %+v", m.BackgroundNudge)
	}
}

// The turn's own mark (or turn end) may land after the first busy tick; a later busy tick picks
// it up instead of keeping the stale one, which would read the session's own end as a new turn.
func TestBackgroundWake_MarkWrittenAfterTheFirstBusyTick(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first func(f *bgWakeFixture)
	}{
		{"mark not written yet", func(f *bgWakeFixture) { f.markOK = false }},
		{"previous turn's mark", func(f *bgWakeFixture) { f.mark = "conv:30" }},
		{"turn end not recorded yet", func(f *bgWakeFixture) { f.turnEnd = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBgWakeFixture(t)
			want := *f
			f.busy = true
			tc.first(f)
			f.tick(0)
			f.markOK, f.mark, f.turnEnd = want.markOK, want.mark, want.turnEnd
			f.tick(secs(15))
			f.busy = false
			for s := 30; s <= 120; s += 15 {
				f.tick(secs(s))
			}
			if len(f.sent) != 1 {
				t.Fatalf("want one nudge, got %d", len(f.sent))
			}
		})
	}
}

// A person's answer can read idle for one tick before the turn it starts shows up; the grace
// must count only unbroken idle, so that tick cannot fire a nudge on the spot.
func TestBackgroundWake_GraceCountsOnlyUnbrokenIdle(t *testing.T) {
	f := newBgWakeFixture(t)
	f.busy = true
	f.tick(0)
	f.busy = false
	f.state = "question"
	for s := 15; s <= 300; s += 15 {
		f.tick(secs(s))
	}
	f.state = "idle"
	f.tick(secs(315))
	f.state = "working"
	f.tick(secs(330))
	if len(f.sent) != 0 {
		t.Fatalf("nudged on a single idle reading after a long person wait: %q", f.sent)
	}
}

func TestBackgroundWake_ExpiredStopArmDoesNotBlock(t *testing.T) {
	f := newBgWakeFixture(t)
	UpdateSessionMeta(f.name, func(m *session.Meta) bool {
		m.StopAfterTurnAt = f.start.Add(-(session.StopArmMaxAge + time.Hour)).Format(time.RFC3339)
		return true
	})
	f.busyThenQuiet(60)
	if len(f.sent) != 1 {
		t.Fatalf("an expired stop arm blocked the nudge: %d sends", len(f.sent))
	}
}

// The rolling hour counts the sends inside it, not a window that only resets after an hour of
// silence.
func TestBackgroundWake_CapIsARollingHour(t *testing.T) {
	f := newBgWakeFixture(t)
	for i, off := range []time.Duration{0, 45 * time.Minute, 90 * time.Minute, 135 * time.Minute} {
		f.start = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC).Add(off)
		f.mark = "conv:" + string(rune('a'+i))
		f.busyThenQuiet(60)
	}
	if len(f.sent) != 4 {
		t.Fatalf("45 min apart never holds 3 in one hour, want 4 sends, got %d", len(f.sent))
	}
}

func TestBackgroundWake_UntrackedQuietSessionsAreNotRead(t *testing.T) {
	f := newBgWakeFixture(t)
	for s := 0; s <= 300; s += 15 {
		f.tick(secs(s))
	}
	if f.reads != 0 {
		t.Fatalf("a session with no background work was read %d times", f.reads)
	}
}

// The ready check must not settle the interim turn end: DriveState would persist idle and fire
// the completion notice and report for a turn that is not over (turn_end_poll.go).
func TestBackgroundWake_DefaultReadsHaveNoTurnEndSideEffects(t *testing.T) {
	f := newBgWakeFixture(t)
	backgroundWakeRead, backgroundWakeReady = backgroundWakeReadDefault, backgroundWakeReadyDefault
	restore := agentreg.SwapForTest(session.KindAgy, fakeWaker{Agent: agentreg.Of(session.KindAgy)})
	t.Cleanup(restore)
	m, _ := session.ReadMeta(f.name)
	sid := session.UUID(m.Dir, m.Name)
	status.PersistRev(sid, "working")
	_ = backgroundWakeRead(m)
	_ = backgroundWakeReady(m)
	if st := status.LiveState(sid); st != "working" {
		t.Fatalf("the watch's reads settled the persisted state to %q", st)
	}
}

// fakeWaker is agy with a state source that reads idle and polled turn ends switched on, so a
// DriveState call would settle the persisted "working".
type fakeWaker struct{ agents.Agent }

func (fakeWaker) WireLive(session.Meta, bool) agents.LiveInfo {
	return agents.LiveInfo{State: "idle", Resumable: true}
}
func (fakeWaker) LiveStateTraits() agents.LiveStateTraits {
	return agents.LiveStateTraits{State: func(session.Meta) string { return "idle" }, PolledTurnEnd: true}
}
func (fakeWaker) FinishedTurnMark(session.Meta) (string, bool) { return "conv:41", true }
func (fakeWaker) InputIdle(session.Meta) bool                  { return true }
