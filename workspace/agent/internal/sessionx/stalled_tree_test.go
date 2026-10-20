package sessionx

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fleetgraph"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// stalledFixture runs the real collect / step / nudge path over fake sessions: the metas are
// real files in a temp dir, liveness and state are tables, and delivery is recorded.
type stalledFixture struct {
	t      *testing.T
	state  map[string]string // name -> wire state; a name missing from alive is stopped
	alive  map[string]bool
	bg     map[string]bool
	turn   map[string]string
	sent   []string
	target []string
	fail   error
	now    time.Time
}

func newStalledFixture(t *testing.T) *stalledFixture {
	t.Helper()
	// Both roots point into the test's own dir: the injection record, the fleet-graph ledger
	// and the peer spools would otherwise land in the real state directory.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	t.Setenv(stalledTreeEnv, "")
	f := &stalledFixture{t: t, state: map[string]string{}, alive: map[string]bool{}, bg: map[string]bool{},
		turn: map[string]string{}, now: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)}
	oa, ow, ob, orr, oi := stalledTreeAlive, stalledTreeWire, stalledTreeBusy, stalledTreeReady, stalledTreeInject
	stalledTreeAlive = func(m session.Meta) bool { return f.alive[m.Name] }
	stalledTreeWire = func(m session.Meta, _ bool) session.Session {
		return session.Session{Name: m.Name, State: f.state[m.Name], BackgroundBusy: f.bg[m.Name], LastTurnEndAt: f.turn[m.Name]}
	}
	stalledTreeBusy = func(session.Meta) bool { return false }
	stalledTreeReady = func(session.Meta) bool { return true }
	stalledTreeInject = func(name, prompt string, _ agents.Origin) error {
		if f.fail != nil {
			return f.fail
		}
		f.target = append(f.target, name)
		f.sent = append(f.sent, prompt)
		return nil
	}
	stalledMu.Lock()
	stalledEpisodes = map[string]*stalledEpisode{}
	stalledMu.Unlock()
	t.Cleanup(func() {
		stalledTreeAlive, stalledTreeWire, stalledTreeBusy, stalledTreeReady, stalledTreeInject = oa, ow, ob, orr, oi
	})
	return f
}

func (f *stalledFixture) session(name, parent string, state string) {
	f.t.Helper()
	m := session.Meta{Name: name, Kind: session.KindClaude, Dir: f.t.TempDir(), OriginSession: parent}
	session.WriteMeta(m)
	f.alive[name] = true
	f.state[name] = state
}

// tickAt runs one sweep at start+d.
func (f *stalledFixture) tickAt(d time.Duration) { stalledTreeTick(f.now.Add(d)) }

func (f *stalledFixture) tree() {
	f.session("parent", "", "idle")
	f.session("kid1", "parent", "idle")
	f.session("kid2", "parent", "idle")
}

func mins(n int) time.Duration { return time.Duration(n) * time.Minute }

func TestStalledTree_FiresOnceAfterQuietPeriod(t *testing.T) {
	f := newStalledFixture(t)
	f.tree()
	f.tickAt(0)
	f.tickAt(mins(14))
	if len(f.sent) != 0 {
		t.Fatalf("fired before the quiet period: %v", f.sent)
	}
	f.tickAt(mins(15))
	if len(f.sent) != 1 || f.target[0] != "parent" {
		t.Fatalf("want exactly one notice to the parent, got %v -> %v", f.target, f.sent)
	}
	for _, want := range []string{"[agent-fleet:notice", "kid1", "kid2", "15 min", "peek_session_output"} {
		if !strings.Contains(f.sent[0], want) {
			t.Errorf("notice lacks %q: %s", want, f.sent[0])
		}
	}
	for d := 16; d <= 240; d += 1 {
		f.tickAt(mins(d))
	}
	if len(f.sent) != 1 {
		t.Fatalf("a quiet tree was nudged %d times, want once", len(f.sent))
	}
}

func TestStalledTree_ChildChangeRearmsButRootOnlyChangeDoesNot(t *testing.T) {
	f := newStalledFixture(t)
	f.tree()
	f.tickAt(0)
	f.tickAt(mins(15))
	if len(f.sent) != 1 {
		t.Fatalf("setup: want 1 notice, got %d", len(f.sent))
	}
	// The nudged parent's own turn: working, then idle again with a new turn end. Not a re-arm.
	f.state["parent"] = "working"
	f.tickAt(mins(16))
	f.state["parent"] = "idle"
	f.turn["parent"] = "2026-10-20T09:17:00Z"
	f.tickAt(mins(17))
	f.tickAt(mins(40))
	if len(f.sent) != 1 {
		t.Fatalf("the parent's reaction to the notice re-armed it: %d notices", len(f.sent))
	}
	// A child that works and finishes is a state change of the tree: a new quiet episode.
	f.state["kid1"] = "working"
	f.tickAt(mins(41))
	f.state["kid1"] = "idle"
	f.turn["kid1"] = "2026-10-20T09:42:00Z"
	f.tickAt(mins(42))
	f.tickAt(mins(56))
	if len(f.sent) != 1 {
		t.Fatalf("second episode fired early: %d", len(f.sent))
	}
	f.tickAt(mins(57))
	if len(f.sent) != 2 {
		t.Fatalf("second quiet episode did not fire: %d", len(f.sent))
	}
}

func TestStalledTree_TurnBetweenTicksResetsTheClock(t *testing.T) {
	f := newStalledFixture(t)
	f.tree()
	f.tickAt(0)
	// A turn that began and ended between two ticks leaves state idle; only the turn end moves.
	f.turn["kid2"] = "2026-10-20T09:10:00Z"
	f.tickAt(mins(10))
	f.tickAt(mins(15))
	if len(f.sent) != 0 {
		t.Fatalf("fired 5 min after a child's turn ended: %v", f.sent)
	}
	f.tickAt(mins(25))
	if len(f.sent) != 1 {
		t.Fatalf("want a notice 15 min after the last change, got %d", len(f.sent))
	}
}

func TestStalledTree_NoNoticeWhenSomethingIsInFlight(t *testing.T) {
	cases := map[string]func(f *stalledFixture){
		"parent asks its user": func(f *stalledFixture) { f.state["parent"] = "question" },
		"plan approval":        func(f *stalledFixture) { f.state["parent"] = "plan" },
		"permission prompt":    func(f *stalledFixture) { f.state["parent"] = "permission" },
		"child working":        func(f *stalledFixture) { f.state["kid1"] = "working" },
		"child waits on user":  func(f *stalledFixture) { f.state["kid2"] = "question" },
		"unknown state":        func(f *stalledFixture) { f.state["kid2"] = "something-new" },
		"background work":      func(f *stalledFixture) { f.bg["kid1"] = true },
		"queued peer to child": func(f *stalledFixture) {
			if err := agents.PutPendingPeer("kid1", agents.PendingPeer{ID: "p", From: "outsider", Message: "m", QueuedAt: time.Now()}); err != nil {
				f.t.Fatal(err)
			}
		},
		"queued peer from child": func(f *stalledFixture) {
			if err := agents.PutPendingPeer("outsider", agents.PendingPeer{ID: "p", From: "kid2", Message: "m", QueuedAt: time.Now()}); err != nil {
				f.t.Fatal(err)
			}
		},
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			f := newStalledFixture(t)
			f.tree()
			arrange(f)
			for d := 0; d <= 120; d += 5 {
				f.tickAt(mins(d))
			}
			if len(f.sent) != 0 {
				t.Fatalf("nudged although %s: %v", name, f.sent)
			}
		})
	}
}

func TestStalledTree_NeverFiresWithoutALiveChild(t *testing.T) {
	f := newStalledFixture(t)
	f.tree()
	f.alive["kid1"], f.alive["kid2"] = false, false
	for d := 0; d <= 120; d += 5 {
		f.tickAt(mins(d))
	}
	if len(f.sent) != 0 {
		t.Fatalf("a tree whose children are all stopped was nudged: %v", f.sent)
	}
	// An archived child is not a child either.
	f.alive["kid1"] = true
	session.WriteMeta(session.Meta{Name: "kid1", Kind: session.KindClaude, Dir: t.TempDir(), OriginSession: "parent", Archived: true})
	for d := 130; d <= 250; d += 5 {
		f.tickAt(mins(d))
	}
	if len(f.sent) != 0 {
		t.Fatalf("a tree with only an archived child was nudged: %v", f.sent)
	}
}

func TestStalledTree_StoppedChildIsNotWaitedOnAndNotNamed(t *testing.T) {
	f := newStalledFixture(t)
	f.tree()
	f.alive["kid2"] = false
	f.tickAt(0)
	f.tickAt(mins(15))
	if len(f.sent) != 1 || strings.Contains(f.sent[0], "kid2") || !strings.Contains(f.sent[0], "kid1") {
		t.Fatalf("want one notice naming only the live child, got %v", f.sent)
	}
}

func TestStalledTree_OnlyTheTopmostLiveParentIsNudged(t *testing.T) {
	f := newStalledFixture(t)
	f.session("root", "", "idle")
	f.session("mid", "root", "idle")
	f.session("leaf", "mid", "idle")
	f.tickAt(0)
	f.tickAt(mins(15))
	if len(f.sent) != 1 || f.target[0] != "root" || !strings.Contains(f.sent[0], "leaf") {
		t.Fatalf("want one notice to root naming the whole subtree, got %v -> %v", f.target, f.sent)
	}
}

func TestStalledTree_FailedDeliveryRetriesLaterAndNeverRepeatsOnSuccess(t *testing.T) {
	f := newStalledFixture(t)
	f.tree()
	f.fail = errors.New("pane gone")
	f.tickAt(0)
	f.tickAt(mins(15))
	f.tickAt(mins(16)) // inside the retry spacing: no hammering
	if len(f.sent) != 0 {
		t.Fatal("sent despite the failure")
	}
	f.fail = nil
	f.tickAt(mins(17))
	if len(f.sent) != 0 {
		t.Fatalf("retried inside the spacing: %v", f.sent)
	}
	f.tickAt(mins(21))
	f.tickAt(mins(30))
	if len(f.sent) != 1 {
		t.Fatalf("want exactly one notice after the retry, got %d", len(f.sent))
	}
}

func TestStalledTree_RootNotReadyDefersWithoutFiring(t *testing.T) {
	f := newStalledFixture(t)
	f.tree()
	ready := false
	stalledTreeReady = func(session.Meta) bool { return ready }
	f.tickAt(0)
	f.tickAt(mins(15))
	if len(f.sent) != 0 {
		t.Fatal("sent into a root that is not ready")
	}
	ready = true
	f.tickAt(mins(21))
	if len(f.sent) != 1 {
		t.Fatalf("want the deferred notice once the root is ready, got %d", len(f.sent))
	}
}

func TestStalledTree_DisabledByEnv(t *testing.T) {
	f := newStalledFixture(t)
	t.Setenv(stalledTreeEnv, "0")
	f.tree()
	f.tickAt(0)
	f.tickAt(mins(600))
	if len(f.sent) != 0 {
		t.Fatalf("fired while disabled: %v", f.sent)
	}
	t.Setenv(stalledTreeEnv, "5")
	f.tickAt(mins(601))
	f.tickAt(mins(606))
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "5 min") {
		t.Fatalf("env override of 5 min not honoured: %v", f.sent)
	}
}

func TestStalledTree_RecordsItsOwnLedgerEventOffTheWire(t *testing.T) {
	f := newStalledFixture(t)
	f.tree()
	f.tickAt(0)
	f.tickAt(mins(15))
	if len(f.sent) != 1 {
		t.Fatal("setup: no notice")
	}
	// The ledger is under the test's HOME; the nudge is stamped with the real clock.
	dir := filepath.Join(paths.AgentStateDir(), "fleet-graph")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, e := range ents {
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		all.Write(b)
	}
	if n := strings.Count(all.String(), `"ev":"stall"`); n != 1 {
		t.Fatalf("want one stall line in the ledger, got %d:\n%s", n, all.String())
	}
	if !strings.Contains(all.String(), `"ev":"instruct"`) || !strings.Contains(all.String(), `"from":"agent"`) {
		t.Fatalf("the delivery should also draw the ordinary instruct arrow from the agent:\n%s", all.String())
	}
	page, err := fleetgraph.BuildPage(0, time.Now().Add(time.Hour).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(page.Activity)
	if strings.Contains(string(wire), `"ev":"stall"`) {
		t.Fatalf("stall leaked onto the frozen fleet-graph wire: %s", wire)
	}
}
