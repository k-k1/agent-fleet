package sessionx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/watch"
)

type fleetFixture struct {
	alive map[string]bool
	wire  map[string]session.Session
	peers bool
	mux   *http.ServeMux
}

func newFleetFixture(t *testing.T) *fleetFixture {
	t.Helper()
	newWatchFixture(t)
	f := &fleetFixture{alive: map[string]bool{}, wire: map[string]session.Session{}}
	oa, ow, op := fleetAlive, fleetWire, fleetPeersOn
	fleetAlive = func(m session.Meta) bool { return f.alive[m.Name] }
	fleetWire = func(m session.Meta, _ bool) session.Session { return f.wire[m.Name] }
	fleetPeersOn = func() bool { return f.peers }
	t.Cleanup(func() { fleetAlive, fleetWire, fleetPeersOn = oa, ow, op })
	useWatchEngine(t, watch.New(watchDiskStore{}, newWatchSink(), &watch.FleetSource{Fleet: watchFleet{}}))
	f.mux = http.NewServeMux()
	f.mux.HandleFunc("POST /sessions/{name}/watches", HandleWatchCreate)
	return f
}

func (f *fleetFixture) create(from, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/sessions/"+from+"/watches", strings.NewReader(body)))
	return rec
}

func TestFleetWatchScope(t *testing.T) {
	f := newFleetFixture(t)
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: "p", Kind: session.KindClaude, Dir: dir})
	session.WriteMeta(session.Meta{Name: "kid", Kind: session.KindClaude, Dir: dir, OriginSession: "p", Origin: session.OriginSession})
	session.WriteMeta(session.Meta{Name: "other", Kind: session.KindClaude, Dir: dir})
	session.WriteMeta(session.Meta{Name: "sh", Kind: session.KindShell, Dir: dir})
	session.WriteMeta(session.Meta{Name: "old", Kind: session.KindClaude, Dir: dir, Archived: true})
	for _, n := range []string{"p", "kid", "other", "sh", "old"} {
		f.alive[n] = true
	}
	body := func(ref string) string {
		return `{"source":"fleet","ref":"` + ref + `","types":["fleet.session.idle"]}`
	}

	if rec := f.create("p", body("kid")); rec.Code != 200 {
		t.Fatalf("a child: %d %s", rec.Code, rec.Body)
	}
	// Peer messaging off: the watch sees what list_child_sessions shows, and nothing else.
	for _, ref := range []string{"other", "sh", "old", "nobody", "p"} {
		if rec := f.create("p", body(ref)); rec.Code != 400 {
			t.Fatalf("%s with peer messaging off: %d %s", ref, rec.Code, rec.Body)
		}
	}
	f.peers = true
	if rec := f.create("p", body("other")); rec.Code != 200 {
		t.Fatalf("a peer with peer messaging on: %d %s", rec.Code, rec.Body)
	}
	// What list_peer_sessions would not show stays refused: a shell, an archived session, itself.
	for _, ref := range []string{"sh", "old", "nobody", "p"} {
		if rec := f.create("p", body(ref)); rec.Code != 400 {
			t.Fatalf("%s with peer messaging on: %d %s", ref, rec.Code, rec.Body)
		}
	}
	// The child asks to tell its parent: the parent is neither visible nor allowed as the subject.
	if rec := f.create("kid", `{"source":"fleet","ref":"p","types":["fleet.session.idle"],"notify":"parent"}`); rec.Code != 400 {
		t.Fatalf("a child watching the parent it reports to: %d %s", rec.Code, rec.Body)
	}
}

func TestFleetWatchObserve(t *testing.T) {
	f := newFleetFixture(t)
	session.WriteMeta(session.Meta{Name: "kid", Kind: session.KindClaude, Dir: t.TempDir()})
	w := watchFleet{}

	if _, ok := w.Observe("ghost"); ok {
		t.Fatal("a missing session was observed")
	}
	f.alive["kid"] = true
	f.wire["kid"] = session.Session{State: "idle", LastTurnEndAt: "2026-11-13T10:00:00Z"}
	if o, ok := w.Observe("kid"); !ok || !o.Alive || !o.Idle || o.LastTurnEndAt != "2026-11-13T10:00:00Z" || o.Limit != "" {
		t.Fatalf("idle: %+v %v", o, ok)
	}
	// What the list does not call idle is not idle: a question, a permission, a working turn.
	for _, st := range []string{"working", "waiting", "blocked"} {
		f.wire["kid"] = session.Session{State: st, LastTurnEndAt: "2026-11-13T10:00:00Z"}
		if o, _ := w.Observe("kid"); o.Idle {
			t.Fatalf("state %q read as idle", st)
		}
	}
	f.alive["kid"] = false
	if o, _ := w.Observe("kid"); o.Alive || o.Idle {
		t.Fatalf("stopped: %+v", o)
	}
	session.WriteMeta(session.Meta{Name: "kid", Kind: session.KindClaude, Dir: t.TempDir(), Archived: true})
	if _, ok := w.Observe("kid"); ok {
		t.Fatal("an archived session was observed")
	}
}

func TestFleetLimitEpisode(t *testing.T) {
	newWatchFixture(t)
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	at := now.Add(-time.Hour).Format(time.RFC3339)
	if got := fleetLimitEpisode("kid", now); got != "" {
		t.Fatalf("no episode: %q", got)
	}
	write := func(st rateLimitState) {
		t.Helper()
		if err := RateLimitStates.Write("kid", st); err != nil {
			t.Fatal(err)
		}
	}
	write(rateLimitState{At: at, ResumeAt: now.Add(time.Hour).Format(time.RFC3339)})
	if got := fleetLimitEpisode("kid", now); got != at {
		t.Fatalf("waiting for the reset: %q", got)
	}
	write(rateLimitState{At: at, ResumeAt: now.Add(-time.Minute).Format(time.RFC3339)})
	if got := fleetLimitEpisode("kid", now); got != "" {
		t.Fatalf("the reset has passed, the sweep has not retired the file yet: %q", got)
	}
	write(rateLimitState{At: at, Spend: true, ResumeAt: now.Add(-time.Minute).Format(time.RFC3339)})
	if got := fleetLimitEpisode("kid", now); got != at {
		t.Fatalf("a spend limit has no instant: %q", got)
	}
	write(rateLimitState{At: at})
	if got := fleetLimitEpisode("kid", now); got != at {
		t.Fatalf("no instant known: %q", got)
	}
}

// A hook-less kind whose state source still shows the previous turn's idle just after a new input
// must not give the new turn an end it has not had: the real end would then reuse that time, and
// the watch would count one turn end for two. Runs through the real wireSession.
func TestFleetWatchStaleIdleDoesNotConsumeTheNextTurnEnd(t *testing.T) {
	m, sid, turnEnded := polledTurnEndFixture(t, "copilot_fleet")
	polledEndSettle = polledEndSettleDefault
	now := time.Now()
	polledEndNow = func() time.Time { return now }
	defer func() { polledEndSettle, polledEndNow = 0, time.Now }()
	oa, op := fleetAlive, fleetPeersOn
	fleetAlive = func(session.Meta) bool { return true }
	fleetPeersOn = func() bool { return false }
	defer func() { fleetAlive, fleetPeersOn = oa, op }()

	turnEnded()                    // turn 1 is over: the source says idle
	status.Persist(sid, "working") // a new input; the source has not caught up
	src := &watch.FleetSource{Fleet: watchFleet{}}
	ctx := watch.WithOwner(watch.WithSubscriber(context.Background(), "parent1"), "parent1")
	res, err := src.Resolve(ctx, m.Name, "")
	if err != nil {
		t.Fatal(err)
	}
	if v := res.Told[watch.FleetIdle]; v != "" {
		t.Fatalf("the stale idle was recorded as a turn end and seeded as told: %q", v)
	}
	subj := res.Subject
	evs, _, _, err := src.Poll(ctx, subj, nil, nil)
	if err != nil || len(evs) != 0 {
		t.Fatalf("a stale idle fired: %v %v", evs, err)
	}

	// The new turn runs, then really ends.
	os.WriteFile(copilot.EventsPath(copilot.SessionID(m)), []byte(`{"type":"user.message"}`+"\n"), 0o600)
	if o, _ := (watchFleet{}).Observe(m.Name); o.Idle {
		t.Fatal("a running turn read as idle")
	}
	now = now.Add(10 * time.Second)
	turnEnded()
	evs, _, _, err = src.Poll(ctx, subj, nil, nil)
	if err != nil || len(evs) != 1 || evs[0].Type != watch.FleetIdle || evs[0].Version == "" || evs[0].Version == res.Told[watch.FleetIdle] {
		t.Fatalf("the real turn end was not told as a new version: %+v %v", evs, err)
	}
}

// The other route that records a polled end, DriveState (get_session_status, the chat chip, the
// mirror), honours the same window with the production setting, and the notification it guards is
// still fired once the window has passed.
func TestFleetWatchDriveStateHonoursTheSettleWindow(t *testing.T) {
	m, sid, turnEnded := polledTurnEndFixture(t, "copilot_fleet_drive")
	polledEndSettle = polledEndSettleDefault
	now := time.Now()
	polledEndNow = func() time.Time { return now }
	defer func() { polledEndSettle, polledEndNow = 0, time.Now }()
	oa, op := fleetAlive, fleetPeersOn
	fleetAlive = func(session.Meta) bool { return true }
	fleetPeersOn = func() bool { return false }
	defer func() { fleetAlive, fleetPeersOn = oa, op }()

	turnEnded()
	status.Persist(sid, "working") // a new input; the source still shows the old idle
	if !turnJustStarted(sid) {
		t.Fatal("control: the new turn is inside the window")
	}
	DriveState(m, true, false)
	if st, _ := status.Read(sid); st.State != "working" || st.TurnEnd {
		t.Fatalf("DriveState settled a turn that had just begun: %+v", st)
	}
	src := &watch.FleetSource{Fleet: watchFleet{}}
	ctx := watch.WithOwner(watch.WithSubscriber(context.Background(), "parent1"), "parent1")
	res, err := src.Resolve(ctx, m.Name, "")
	if err != nil {
		t.Fatal(err)
	}
	if v := res.Told[watch.FleetIdle]; v != "" {
		t.Fatalf("the stale idle became a turn end through DriveState: %q", v)
	}

	// Past the window the same idle is the turn's end, and the notification gate fires once.
	now = now.Add(2 * polledEndSettleDefault)
	DriveState(m, true, false)
	if st, _ := status.Read(sid); st.State != "idle" || !st.TurnEnd {
		t.Fatalf("the end after the window was not recorded: %+v", st)
	}
}
