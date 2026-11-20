package sessionx

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// bgWakeFixture runs the real tick / observe / verdict / send path over one fake agy session:
// the meta is a real file in a temp dir, the live readings are fields, delivery is recorded.
type bgWakeFixture struct {
	t       *testing.T
	name    string
	alive   bool
	state   string
	busy    bool
	turnEnd string
	mark    string
	markOK  bool
	ready   bool
	queued  bool
	fail    error
	sent    []string
	start   time.Time
}

func newBgWakeFixture(t *testing.T) *bgWakeFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	f := &bgWakeFixture{t: t, name: "agy1", alive: true, state: "idle", turnEnd: "2026-11-20T01:55:25+09:00",
		mark: "conv:41", markOK: true, ready: true, start: time.Date(2026, 11, 20, 1, 55, 30, 0, time.UTC)}
	session.WriteMeta(session.Meta{Name: f.name, Kind: session.KindAgy, Dir: t.TempDir()})
	oa, ow, or, om, oq, oi, oo := backgroundWakeAlive, backgroundWakeWire, backgroundWakeReady, backgroundWakeMark,
		backgroundWakeQueued, backgroundWakeInject, backgroundWakeOptedIn
	backgroundWakeAlive = func(session.Meta) bool { return f.alive }
	backgroundWakeWire = func(m session.Meta, _ bool) session.Session {
		return session.Session{Name: m.Name, State: f.state, BackgroundBusy: f.busy, LastTurnEndAt: f.turnEnd, Alive: f.alive}
	}
	backgroundWakeReady = func(session.Meta) bool { return f.ready }
	backgroundWakeMark = func(session.Meta) (string, bool) { return f.mark, f.markOK }
	backgroundWakeQueued = func(string) bool { return f.queued }
	backgroundWakeInject = func(name, prompt string, _ agents.Origin) error {
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
		backgroundWakeAlive, backgroundWakeWire, backgroundWakeReady, backgroundWakeMark,
			backgroundWakeQueued, backgroundWakeInject, backgroundWakeOptedIn = oa, ow, or, om, oq, oi, oo
	})
	return f
}

func (f *bgWakeFixture) tick(d time.Duration) { backgroundWakeTick(f.start.Add(d)) }

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
	if m.BackgroundNudge == nil || m.BackgroundNudge.Mark != "conv:41" || m.BackgroundNudge.Count != 1 {
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
			UpdateSessionMeta(f.name, func(m *session.Meta) bool { m.StopAfterTurnAt = "2026-11-20T01:56:00Z"; return true })
		}},
		{"peer message queued", func(f *bgWakeFixture) { f.queued = true }},
		{"mark unreadable", func(f *bgWakeFixture) { f.markOK = false }},
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
	f.fail = nil
	for s := 1815; s <= 2400; s += 15 {
		f.tick(secs(s))
	}
	if len(f.sent) != 0 {
		t.Fatalf("kept retrying past the bound: %q", f.sent)
	}
	if m, _ := session.ReadMeta(f.name); m.BackgroundNudge != nil {
		t.Fatalf("a failed delivery was recorded as a nudge: %+v", m.BackgroundNudge)
	}
}

func TestBackgroundWake_OtherKindsAreLeftAlone(t *testing.T) {
	f := newBgWakeFixture(t)
	backgroundWakeOptedIn = func(string) bool { return false }
	f.busyThenQuiet(300)
	if len(f.sent) != 0 {
		t.Fatalf("nudged a kind that did not opt in: %q", f.sent)
	}
}
