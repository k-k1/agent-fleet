package sessionx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/watch"
)

type watchFixture struct {
	alive, ready map[string]bool
	sent         []string
	started      []string
}

func newWatchFixture(t *testing.T) *watchFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	f := &watchFixture{alive: map[string]bool{}, ready: map[string]bool{}}
	oa, or, ob, oi, os := watchAlive, watchReady, watchBusy, watchInject, watchStart
	watchAlive = func(m session.Meta) bool { return f.alive[m.Name] }
	watchReady = func(m session.Meta) bool { return f.ready[m.Name] }
	watchBusy = func(session.Meta) bool { return false }
	watchInject = func(name, prompt string, _ agents.Origin) error {
		f.sent = append(f.sent, name+"|"+prompt)
		return nil
	}
	watchStart = func(name string) error { f.started = append(f.started, name); return nil }
	t.Cleanup(func() { watchAlive, watchReady, watchBusy, watchInject, watchStart = oa, or, ob, oi, os })
	return f
}

func TestWatchSinkWaitsForTwoReadyLooksAndStartsAStoppedOwner(t *testing.T) {
	f := newWatchFixture(t)
	session.WriteMeta(session.Meta{Name: "a", Kind: session.KindClaude, Dir: t.TempDir()})
	s := newWatchSink()
	sub := watch.Subscription{ID: "w1", Owner: "a", Until: watch.UntilFirst}
	ev := watch.Event{Source: "github", Subject: "github:o/r#1", Type: "pr.merged", Summary: "PR o/r#1 was merged."}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

	if got := s.Deliver(sub, ev, now); got != watch.NotYet || len(f.started) != 1 {
		t.Fatalf("stopped owner: %v started=%v", got, f.started)
	}
	if s.Deliver(sub, ev, now.Add(5*time.Second)); len(f.started) != 1 {
		t.Fatal("started the same owner twice")
	}
	f.alive["a"], f.ready["a"] = true, true
	if got := s.Deliver(sub, ev, now.Add(10*time.Second)); got != watch.NotYet {
		t.Fatalf("first ready look delivered: %v", got)
	}
	if got := s.Deliver(sub, ev, now.Add(15*time.Second)); got != watch.Delivered || len(f.sent) != 1 {
		t.Fatalf("second ready look: %v sent=%v", got, f.sent)
	}
	if !strings.HasPrefix(f.sent[0], "a|[agent-fleet:notice kind=watch source=github type=pr.merged subject=github:o/r#1 watch=w1 reply=none] ") {
		t.Fatalf("notice = %q", f.sent[0])
	}
}

func TestWatchSinkEndsWithAnArchivedOwner(t *testing.T) {
	newWatchFixture(t)
	session.WriteMeta(session.Meta{Name: "old", Kind: session.KindClaude, Dir: t.TempDir(), Archived: true})
	s := newWatchSink()
	if s.OwnerExists("old") || s.OwnerExists("missing") {
		t.Fatal("an archived or missing owner exists")
	}
	if got := s.Deliver(watch.Subscription{Owner: "old"}, watch.Event{}, time.Now()); got != watch.Gone {
		t.Fatalf("archived owner: %v", got)
	}
}

type stubSource struct{}

func (stubSource) Kind() string    { return "github" }
func (stubSource) Types() []string { return []string{"pr.merged"} }
func (stubSource) Resolve(_ context.Context, ref, def string) (watch.Resolved, error) {
	if ref == "#1" && def == "" {
		return watch.Resolved{}, &watch.ErrRefused{Reason: "no repo"}
	}
	return watch.Resolved{Subject: "github:o/r#1"}, nil
}
func (stubSource) Poll(context.Context, watch.SubjectID, json.RawMessage) ([]watch.Event, json.RawMessage, watch.Hint, error) {
	return nil, nil, watch.Hint{}, nil
}

func useWatchEngine(t *testing.T, e *watch.Engine) {
	t.Helper()
	watchEngineOnce = sync.Once{}
	watchEngineOnce.Do(func() { watchEngineInst = e })
	t.Cleanup(func() { watchEngineOnce = sync.Once{}; watchEngineInst = nil })
}

func TestWatchRoutesCreateListDelete(t *testing.T) {
	newWatchFixture(t)
	session.WriteMeta(session.Meta{Name: "a", Kind: session.KindClaude, Dir: t.TempDir()})
	useWatchEngine(t, watch.New(watchDiskStore{}, newWatchSink(), stubSource{}))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions/{name}/watches", HandleWatchCreate)
	mux.HandleFunc("GET /sessions/{name}/watches", HandleWatchList)
	mux.HandleFunc("DELETE /sessions/{name}/watches/{id}", HandleWatchDelete)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}

	if rec := do("POST", "/sessions/a/watches", `{"ref":"o/r#1","types":["pr.closed"]}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "cannot report") {
		t.Fatalf("unoffered type: %d %s", rec.Code, rec.Body)
	}
	if rec := do("POST", "/sessions/ghost/watches", `{"ref":"o/r#1","types":["pr.merged"]}`); rec.Code != 404 {
		t.Fatalf("unknown session: %d", rec.Code)
	}
	rec := do("POST", "/sessions/a/watches", `{"ref":"o/r#1","types":["pr.merged"]}`)
	if rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var created struct{ Watch watch.Subscription }
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.Watch.ID == "" || created.Watch.Subject != "github:o/r#1" {
		t.Fatalf("created = %+v", created)
	}
	if rec := do("GET", "/sessions/a/watches", ""); !strings.Contains(rec.Body.String(), created.Watch.ID) {
		t.Fatalf("list: %s", rec.Body)
	}
	if b, ok := watchStateStore.Read(watchStateKey); !ok || len(b.Subs) != 1 {
		t.Fatalf("state not on disk: %+v %v", b, ok)
	}
	if rec := do("DELETE", "/sessions/b/watches/"+created.Watch.ID, ""); rec.Code != 404 {
		t.Fatalf("another session ended a watch: %d", rec.Code)
	}
	if rec := do("DELETE", "/sessions/a/watches/"+created.Watch.ID, ""); rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := do("GET", "/sessions/a/watches", ""); strings.Contains(rec.Body.String(), created.Watch.ID) {
		t.Fatalf("still listed: %s", rec.Body)
	}
}
