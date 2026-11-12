package sessionx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
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
