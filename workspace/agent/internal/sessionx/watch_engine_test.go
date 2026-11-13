package sessionx

import (
	"context"
	"encoding/json"
	"fmt"
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
	if err := s.Inject(sub, []watch.Event{ev}); err != nil || len(f.sent) != 1 {
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
	if got := s.Ready("a", now.Add(time.Second)); got != watch.Unreachable {
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
	if got := s.Ready("old", time.Now()); got != watch.Unreachable {
		t.Fatalf("archived owner: %v", got)
	}
	if got := s.Ready("missing", time.Now()); got != watch.Gone {
		t.Fatalf("missing owner: %v", got)
	}
}

func TestWatchSinkWordsABuiltinByItsPurpose(t *testing.T) {
	f := newWatchFixture(t)
	s := newWatchSink()
	sub := watch.Subscription{ID: "b:test:github:o/r#1", Owner: "a", Builtin: "test-purpose"}
	evs := []watch.Event{{Type: "pr.checks.failed", Version: "h"}, {Type: "pr.review.changes_requested", Version: "1"}}
	if err := s.Inject(sub, evs); err == nil {
		t.Fatal("a built-in without wording was injected")
	}
	if err := s.Notify(sub, evs); err == nil {
		t.Fatal("a built-in without a member notification reported success")
	}
	var got []watch.Event
	builtinNoticeText["test-purpose"] = func(_ watch.Subscription, e []watch.Event) string { got = e; return "worded" }
	builtinMemberNotice["test-purpose"] = func(_ watch.Subscription, e []watch.Event) error { got = e; return nil }
	t.Cleanup(func() { delete(builtinNoticeText, "test-purpose"); delete(builtinMemberNotice, "test-purpose") })
	if err := s.Inject(sub, evs); err != nil || len(f.sent) != 1 || f.sent[0] != "a|worded" || len(got) != 2 {
		t.Fatalf("inject: %v sent=%v got=%v", err, f.sent, got)
	}
	got = nil
	if err := s.Notify(sub, evs); err != nil || len(got) != 2 {
		t.Fatalf("notify: %v got=%v", err, got)
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

func TestABuiltinsSubjectIsTheOneASessionsWatchGets(t *testing.T) {
	// A session's own watch covers a built-in only on the same subject: both lower-case the repo.
	for _, c := range []struct{ provider, key, ref string }{
		{"github", "O/R#1", "O/R#1"},
		{"bitbucket", "W/X#2", "https://bitbucket.org/W/X/pull-requests/2"},
	} {
		var repo string
		var n int
		var ok bool
		if c.provider == "github" {
			repo, n, ok = watch.ParseGitHubRef(c.ref, "")
		} else {
			repo, n, ok = watch.ParseBitbucketRef(c.ref, "")
		}
		if want := watch.SubjectID(fmt.Sprintf("%s:%s#%d", c.provider, repo, n)); !ok || prSubject(c.provider, c.key) != want {
			t.Errorf("prSubject(%s, %s) = %s, a watch gets %s", c.provider, c.key, prSubject(c.provider, c.key), want)
		}
	}
}

type bbStub struct{ stubSource }

func (bbStub) Kind() string { return "bitbucket" }
func (bbStub) Resolve(context.Context, string, string) (watch.Resolved, error) {
	return watch.Resolved{Subject: "bitbucket:w/x#2"}, nil
}

type stubActions struct{ stubSource }

func (stubActions) Kind() string { return watch.ActionsKind }
func (stubActions) Types() []string {
	return []string{watch.EventRunCompleted, watch.EventRunFailed}
}
func (stubActions) TypeAliases() map[string]string {
	return map[string]string{"github.workflow_run": "ci.run"}
}
func (stubActions) Resolve(context.Context, string, string) (watch.Resolved, error) {
	return watch.Resolved{Subject: "github:o/r/actions/runs/5"}, nil
}

type stubPipelines struct{}

func (stubPipelines) Kind() string    { return watch.PipelinesKind }
func (stubPipelines) Types() []string { return []string{watch.EventRunCompleted, watch.EventRunFailed} }
func (stubPipelines) Poll(context.Context, watch.SubjectID, json.RawMessage, []string) ([]watch.Event, json.RawMessage, watch.Hint, error) {
	return nil, nil, watch.Hint{}, nil
}
func (stubPipelines) Resolve(context.Context, string, string) (watch.Resolved, error) {
	return watch.Resolved{Subject: "bitbucket:w/r/pipelines/runs/5"}, nil
}

// A Pipelines run URL goes to its source whatever the source field says; the bare forms, which
// are the Actions shapes, go there only with source bitbucket.
func TestWatchRoutesAPipelinesReferenceToItsSource(t *testing.T) {
	newWatchFixture(t)
	session.WriteMeta(session.Meta{Name: "a", Kind: session.KindClaude, Dir: t.TempDir()})
	useWatchEngine(t, watch.New(watchDiskStore{}, newWatchSink(), stubSource{}, stubActions{}, stubPipelines{}))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions/{name}/watches", HandleWatchCreate)
	do := func(body string) (int, watch.Subscription) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/sessions/a/watches", strings.NewReader(body)))
		var c struct{ Watch watch.Subscription }
		_ = json.Unmarshal(rec.Body.Bytes(), &c)
		return rec.Code, c.Watch
	}
	for _, body := range []string{
		`{"ref":"https://bitbucket.org/w/r/pipelines/results/5","types":["ci.run.completed"]}`,
		`{"source":"bitbucket","ref":"https://bitbucket.org/w/r/pipelines/results/5","types":["ci.run.*"]}`,
		`{"source":"bitbucket","ref":"w/r:run/5","types":["ci.run.failed"]}`,
		`{"source":"bitbucket","ref":"w/r:pipelines@main","types":["ci.run.completed"]}`,
	} {
		if code, w := do(body); code != 200 || w.Source != watch.PipelinesKind {
			t.Fatalf("%s: %d %+v", body, code, w)
		}
	}
	// The same bare shape with the default source is still an Actions run.
	if code, w := do(`{"ref":"o/r:run/5","types":["ci.run.completed"]}`); code != 200 || w.Source != watch.ActionsKind {
		t.Fatalf("actions shape: %d %+v", code, w)
	}
	// A pull request on Bitbucket stays a pull request.
	if code, w := do(`{"source":"bitbucket","ref":"w/r#1","types":["pr.merged"]}`); code == 200 && w.Source == watch.PipelinesKind {
		t.Fatalf("bitbucket pr rerouted: %d %+v", code, w)
	}
}

// A workflow run reference sent with the default source is a run watch; a pull request
// reference stays a pull request watch.
func TestWatchRoutesAnActionsReferenceToItsSource(t *testing.T) {
	newWatchFixture(t)
	session.WriteMeta(session.Meta{Name: "a", Kind: session.KindClaude, Dir: t.TempDir()})
	useWatchEngine(t, watch.New(watchDiskStore{}, newWatchSink(), stubSource{}, stubActions{}))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions/{name}/watches", HandleWatchCreate)
	do := func(body string) (int, watch.Subscription) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/sessions/a/watches", strings.NewReader(body)))
		var c struct{ Watch watch.Subscription }
		_ = json.Unmarshal(rec.Body.Bytes(), &c)
		return rec.Code, c.Watch
	}
	code, w := do(`{"ref":"https://github.com/o/r/actions/runs/5","types":["github.workflow_run.completed"]}`)
	if code != 200 || w.Source != watch.ActionsKind {
		t.Fatalf("run ref: %d %+v", code, w)
	}
	code, w = do(`{"source":"github","ref":"o/r:ci.yml@main","types":["github.workflow_run.*"]}`)
	if code != 200 || w.Source != watch.ActionsKind {
		t.Fatalf("workflow ref: %d %+v", code, w)
	}
	code, w = do(`{"ref":"o/r#1","types":["pr.merged"]}`)
	if code != 200 || w.Source != "github" {
		t.Fatalf("pr ref: %d %+v", code, w)
	}
	// A PR type asked for a run is refused by the source's own type list.
	if code, _ = do(`{"ref":"o/r:run/5","types":["pr.merged"]}`); code != 400 {
		t.Fatalf("pr type on a run: %d", code)
	}
}

type stubReleases struct {
	stubSource
	kind, subject, typ string
}

func (s stubReleases) Kind() string    { return s.kind }
func (s stubReleases) Types() []string { return []string{s.typ} }
func (s stubReleases) Resolve(context.Context, string, string) (watch.Resolved, error) {
	return watch.Resolved{Subject: watch.SubjectID(s.subject)}, nil
}

// A releases or tags reference with the default source goes to its own source, so a releases
// watch cannot be asked for a tag type; a pull request reference is left alone.
func TestWatchRoutesAReleasesReferenceToItsSource(t *testing.T) {
	newWatchFixture(t)
	session.WriteMeta(session.Meta{Name: "a", Kind: session.KindClaude, Dir: t.TempDir()})
	useWatchEngine(t, watch.New(watchDiskStore{}, newWatchSink(), stubSource{},
		stubReleases{kind: watch.ReleasesKind, subject: "github:o/r/releases/after/1-2", typ: watch.EventReleasePublished},
		stubReleases{kind: watch.TagsKind, subject: "github:o/r/tags/after/2", typ: watch.EventTagCreated}))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions/{name}/watches", HandleWatchCreate)
	do := func(body string) (int, watch.Subscription) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/sessions/a/watches", strings.NewReader(body)))
		var c struct{ Watch watch.Subscription }
		_ = json.Unmarshal(rec.Body.Bytes(), &c)
		return rec.Code, c.Watch
	}
	if code, w := do(`{"ref":"o/r:releases@v2.*","types":["github.release.published"]}`); code != 200 || w.Source != watch.ReleasesKind {
		t.Fatalf("releases ref: %d %+v", code, w)
	}
	if code, w := do(`{"ref":"https://github.com/o/r/releases","types":["github.release.*"]}`); code != 200 || w.Source != watch.ReleasesKind {
		t.Fatalf("releases url: %d %+v", code, w)
	}
	if code, w := do(`{"source":"github","ref":"o/r:tags@v*","types":["github.tag.created"]}`); code != 200 || w.Source != watch.TagsKind {
		t.Fatalf("tags ref: %d %+v", code, w)
	}
	if code, _ := do(`{"ref":"o/r:releases","types":["github.tag.created"]}`); code == 200 {
		t.Fatal("a releases watch accepted a tag event type")
	}
	if code, w := do(`{"ref":"o/r#1","types":["pr.merged"]}`); code != 200 || w.Source != "github" {
		t.Fatalf("pr ref: %d %+v", code, w)
	}
}
