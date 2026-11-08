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
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

	if got := s.Ready("a", now); got != watch.Wait || len(f.started) != 1 {
		t.Fatalf("stopped owner: %v started=%v", got, f.started)
	}
	if s.Ready("a", now.Add(5*time.Second)); len(f.started) != 1 {
		t.Fatal("started the same owner twice")
	}
	f.alive["a"], f.ready["a"] = true, true
	if got := s.Ready("a", now.Add(10*time.Second)); got != watch.Wait {
		t.Fatalf("first ready look: %v", got)
	}
	if got := s.Ready("a", now.Add(15*time.Second)); got != watch.Ready {
		t.Fatalf("second ready look: %v", got)
	}
	if got := s.Ready("a", now.Add(20*time.Second)); got != watch.Wait {
		t.Fatalf("the look after a delivery should start over: %v", got)
	}
	sub := watch.Subscription{ID: "w1", Owner: "a", Until: watch.UntilFirst}
	ev := watch.Event{Source: "github", Subject: "github:o/r#1", Type: "pr.merged", Summary: "PR o/r#1 was merged."}
	if err := s.Inject(sub, ev); err != nil || len(f.sent) != 1 {
		t.Fatalf("inject: %v sent=%v", err, f.sent)
	}
	if !strings.HasPrefix(f.sent[0], "a|[agent-fleet:notice kind=watch source=github type=pr.merged subject=github:o/r#1 watch=w1 reply=none] ") {
		t.Fatalf("notice = %q", f.sent[0])
	}
}

func TestWatchSinkGoneOwnerFreesTheStartSlot(t *testing.T) {
	f := newWatchFixture(t)
	session.WriteMeta(session.Meta{Name: "a", Kind: session.KindClaude, Dir: t.TempDir()})
	session.WriteMeta(session.Meta{Name: "b", Kind: session.KindClaude, Dir: t.TempDir()})
	s := newWatchSink()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	s.Ready("a", now) // starts a
	session.WriteMeta(session.Meta{Name: "a", Kind: session.KindClaude, Dir: t.TempDir(), Archived: true})
	if got := s.Ready("a", now.Add(time.Second)); got != watch.Gone {
		t.Fatalf("archived owner: %v", got)
	}
	s.Ready("b", now.Add(2*time.Second))
	if strings.Join(f.started, ",") != "a,b" {
		t.Fatalf("b waited for an archived owner's start slot: started=%v", f.started)
	}
}

func TestWatchSinkEndsWithAnArchivedOwner(t *testing.T) {
	newWatchFixture(t)
	session.WriteMeta(session.Meta{Name: "old", Kind: session.KindClaude, Dir: t.TempDir(), Archived: true})
	s := newWatchSink()
	if s.OwnerExists("old") || s.OwnerExists("missing") {
		t.Fatal("an archived or missing owner exists")
	}
	if got := s.Ready("old", time.Now()); got != watch.Gone {
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
func (stubSource) Poll(context.Context, watch.SubjectID, json.RawMessage, []string) ([]watch.Event, json.RawMessage, watch.Hint, error) {
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
	session.WriteMeta(session.Meta{Name: "b", Kind: session.KindClaude, Dir: t.TempDir()})
	if rec := do("DELETE", "/sessions/b/watches/"+created.Watch.ID, ""); rec.Code != 404 {
		t.Fatalf("another session ended a watch: %d", rec.Code)
	}
	if rec := do("GET", "/sessions/b/watches", ""); strings.Contains(rec.Body.String(), created.Watch.ID) {
		t.Fatalf("another session lists a's watch: %s", rec.Body)
	}
	for _, h := range []string{"721", "9223372036"} {
		if rec := do("POST", "/sessions/a/watches", `{"ref":"o/r#1","types":["pr.merged"],"expires_in_hours":`+h+`}`); rec.Code != 400 {
			t.Fatalf("expires_in_hours %s: %d %s", h, rec.Code, rec.Body)
		}
	}
	if rec := do("DELETE", "/sessions/a/watches/"+created.Watch.ID, ""); rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := do("GET", "/sessions/a/watches", ""); strings.Contains(rec.Body.String(), created.Watch.ID) {
		t.Fatalf("still listed: %s", rec.Body)
	}
}

func TestWatchNotifyParentBelongsToTheParent(t *testing.T) {
	newWatchFixture(t)
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: "p", Kind: session.KindClaude, Dir: dir})
	session.WriteMeta(session.Meta{Name: "c", Kind: session.KindClaude, Dir: dir, OriginSession: "p", Origin: session.OriginSession})
	session.WriteMeta(session.Meta{Name: "h", Kind: session.KindClaude, Dir: dir, OriginSession: "p", Origin: session.OriginUser})
	useWatchEngine(t, watch.New(watchDiskStore{}, newWatchSink(), stubSource{}))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions/{name}/watches", HandleWatchCreate)
	mux.HandleFunc("GET /sessions/{name}/watches", HandleWatchList)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	rec := do("POST", "/sessions/c/watches", `{"ref":"o/r#1","types":["pr.merged"],"notify":"parent"}`)
	if rec.Code != 200 {
		t.Fatalf("child notify parent: %d %s", rec.Code, rec.Body)
	}
	var created struct{ Watch watch.Subscription }
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.Watch.Owner != "p" || created.Watch.Requester != "c" {
		t.Fatalf("owner %q requester %q", created.Watch.Owner, created.Watch.Requester)
	}
	if !strings.Contains(do("GET", "/sessions/p/watches", "").Body.String(), created.Watch.ID) ||
		strings.Contains(do("GET", "/sessions/c/watches", "").Body.String(), created.Watch.ID) {
		t.Fatal("the watch is not listed by the parent alone")
	}
	mux.HandleFunc("DELETE /sessions/{name}/watches/{id}", HandleWatchDelete)
	if rec := do("DELETE", "/sessions/c/watches/"+created.Watch.ID, ""); rec.Code != 404 {
		t.Fatalf("the child ended its parent's watch: %d", rec.Code)
	}
	// A handoff successor and a top-level session have no parent to notify.
	for _, n := range []string{"h", "p"} {
		if rec := do("POST", "/sessions/"+n+"/watches", `{"ref":"o/r#1","types":["pr.merged"],"notify":"parent"}`); rec.Code != 400 {
			t.Fatalf("%s notify parent: %d %s", n, rec.Code, rec.Body)
		}
	}
	// The child being archived does not end it; the parent being archived does.
	session.WriteMeta(session.Meta{Name: "c", Kind: session.KindClaude, Dir: dir, Archived: true})
	watchEngine().Tick(context.Background())
	if !strings.Contains(do("GET", "/sessions/p/watches", "").Body.String(), created.Watch.ID) {
		t.Fatal("archiving the child ended the parent's watch")
	}
	session.WriteMeta(session.Meta{Name: "p", Kind: session.KindClaude, Dir: dir, Archived: true})
	watchEngine().Tick(context.Background())
	if l := watchEngine().List("p"); len(l) != 0 {
		t.Fatalf("archiving the parent left its watch: %+v", l)
	}
}

func TestWatchingPRMatchesTheSubjectCaseInsensitively(t *testing.T) {
	newWatchFixture(t)
	session.WriteMeta(session.Meta{Name: "a", Kind: session.KindClaude, Dir: t.TempDir()})
	e := watch.New(watchDiskStore{}, newWatchSink(), stubSource{})
	useWatchEngine(t, e)
	if _, err := e.Subscribe(context.Background(), watch.Request{Owner: "a", Source: "github", Ref: "o/r#1", Types: []string{"pr.merged"}}); err != nil {
		t.Fatal(err)
	}
	if !watchingPR("a", "github", "O/R#1", "pr.merged") || watchingPR("a", "github", "o/r#1", "pr.checks.failed") ||
		watchingPR("b", "github", "o/r#1", "pr.merged") || watchingPR("a", "bitbucket", "o/r#1", "pr.merged") {
		t.Fatal("watchingPR answered wrongly")
	}
}
