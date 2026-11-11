package watch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	plList5  = "/2.0/repositories/w/r/pipelines/?sort=-created_on&pagelen=5"
	plList50 = "/2.0/repositories/w/r/pipelines/?sort=-created_on&pagelen=50"
)

func newTestPipelines(t *testing.T, f *fakeGitHub) *BitbucketPipelines {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	p := NewBitbucketPipelines(func() (string, error) { return "Bearer tok", nil }, nil)
	p.API = srv.URL
	return p
}

func plRunBody(build int64, state, result, created, completed, ref, pattern string) map[string]any {
	st := map[string]any{"name": state}
	if result != "" {
		st["result"] = map[string]any{"name": result}
	}
	return map[string]any{
		"uuid": "{u-" + strconv.FormatInt(build, 10) + "}", "build_number": build, "created_on": created, "completed_on": completed,
		"state": st, "target": map[string]any{"ref_name": ref, "selector": map[string]any{"pattern": pattern}},
	}
}

func plRunPathOf(build int64) string {
	return "/2.0/repositories/w/r/pipelines/" + strconv.FormatInt(build, 10)
}

func TestParsePipelinesRef(t *testing.T) {
	cases := []struct {
		ref  string
		want pipelinesRef
		ok   bool
	}{
		{"https://bitbucket.org/W/R/pipelines/results/123", pipelinesRef{repo: "W/R", build: 123}, true},
		{"https://bitbucket.org/w/r/pipelines/results/123/steps/{x}", pipelinesRef{repo: "w/r", build: 123}, true},
		{"w/r:run/123", pipelinesRef{repo: "w/r", build: 123}, true},
		{"run/123", pipelinesRef{repo: "x/x", build: 123}, true},
		{"https://bitbucket.org/w/r/pipelines", pipelinesRef{repo: "w/r"}, true},
		{"w/r:pipelines", pipelinesRef{repo: "w/r"}, true},
		{"w/r:pipelines@main", pipelinesRef{repo: "w/r", branch: "main"}, true},
		{"w/r:pipelines/nightly@release/1.x", pipelinesRef{repo: "w/r", name: "nightly", branch: "release/1.x"}, true},
		{"pipelines/nightly", pipelinesRef{repo: "x/x", name: "nightly"}, true},
		// Pull request references stay with the bitbucket source.
		{"w/r#12", pipelinesRef{}, false},
		{"#12", pipelinesRef{}, false},
		{"https://bitbucket.org/w/r/pull-requests/12", pipelinesRef{}, false},
		{"w/r:pipelines@a b", pipelinesRef{}, false},
	}
	for _, c := range cases {
		got, ok := matchPipelinesRef(c.ref, "x/x")
		if ok != c.ok || got != c.want {
			t.Errorf("%q = %+v %v, want %+v %v", c.ref, got, ok, c.want, c.ok)
		}
	}
	if !LooksLikePipelinesRef("https://bitbucket.org/w/r/pipelines/results/5") || LooksLikePipelinesRef("w/r:run/5") {
		t.Error("only the URL form is unambiguous without the source")
	}
	if !LooksLikePipelinesShortRef("w/r:run/5") || LooksLikePipelinesShortRef("w/r#5") {
		t.Error("short form")
	}
	if _, err := parsePipelinesRef("w/r:pipelines@a..b", ""); err == nil {
		t.Error("a branch with .. was accepted")
	}
	if _, err := parsePipelinesRef("pipelines", ""); err == nil {
		t.Error("no repository was accepted")
	}
	if r, err := parsePipelinesRef("https://bitbucket.org/W/R/pipelines/results/5", ""); err != nil || r.repo != "w/r" {
		t.Errorf("repo not lower-cased: %+v %v", r, err)
	}
}

func TestPipelinesShareTheCITypesAndRefusePRTypes(t *testing.T) {
	p, a := &BitbucketPipelines{}, &GitHubActions{}
	if strings.Join(p.Types(), ",") != strings.Join(a.Types(), ",") {
		t.Fatalf("types differ: %v vs %v", p.Types(), a.Types())
	}
	if err := checkTypes([]string{"pr.merged"}, p.Types()); err == nil {
		t.Fatal("a run watch accepted pr.merged")
	}
	if err := checkTypes([]string{"ci.run.*"}, p.Types()); err != nil {
		t.Fatal(err)
	}
	if err := checkTypes([]string{EventRunFailed}, (&Bitbucket{}).Types()); err == nil {
		t.Fatal("a PR watch accepted a run type")
	}
}

func TestResolvePipelineRun(t *testing.T) {
	f := newFakeGitHub()
	f.bodies[plRunPathOf(55)] = plRunBody(55, "COMPLETED", "SUCCESSFUL", "2026-11-10T01:00:00Z", "2026-11-10T01:05:00Z", "main", "")
	f.status[plRunPathOf(57)] = http.StatusForbidden
	p := newTestPipelines(t, f)
	for ref, want := range map[string]SubjectID{
		"https://bitbucket.org/w/r/pipelines/results/55": "bitbucket:w/r/pipelines/runs/55",
		"w/r:run/55": "bitbucket:w/r/pipelines/runs/55",
	} {
		res, err := p.Resolve(context.Background(), ref, "")
		if err != nil || res.Subject != want || !TokenOK(string(res.Subject)) || res.URL != "https://bitbucket.org/w/r/pipelines/results/55" {
			t.Errorf("%s -> %+v %v", ref, res, err)
		}
	}
	var ref *ErrRefused
	if _, err := p.Resolve(context.Background(), "w/r:run/56", ""); !errors.As(err, &ref) || !strings.Contains(ref.Reason, "can read") {
		t.Fatalf("missing run: %v", err)
	}
	if _, err := p.Resolve(context.Background(), "w/r:run/57", ""); !errors.As(err, &ref) || !strings.Contains(ref.Reason, "Pipelines read scope") {
		t.Fatalf("scope: %v", err)
	}
	// The token is sent as given and never appears in an error.
	for _, a := range f.tokens {
		if a != "Bearer tok" {
			t.Errorf("authorization %q", a)
		}
	}
	if ref != nil && strings.Contains(ref.Reason, "tok") {
		t.Errorf("error leaks token: %s", ref.Reason)
	}
}

func TestPollPipelineRun(t *testing.T) {
	f := newFakeGitHub()
	const subj = SubjectID("bitbucket:w/r/pipelines/runs/55")
	f.bodies[plRunPathOf(55)] = plRunBody(55, "IN_PROGRESS", "", "2026-11-10T01:00:00Z", "", "main", "")
	p := newTestPipelines(t, f)
	ctx := context.Background()

	evs, cur, hint, err := p.Poll(ctx, subj, nil, nil)
	if err != nil || len(evs) != 0 || hint.After != plBusyAfter || hint.Settled {
		t.Fatalf("in progress: %v %v %+v", evs, err, hint)
	}
	f.bodies[plRunPathOf(55)] = plRunBody(55, "PENDING", "", "2026-11-10T01:00:00Z", "", "main", "")
	if _, _, hint, _ = p.Poll(ctx, subj, cur, nil); hint.After != plQueuedAfter {
		t.Fatalf("pending hint %+v", hint)
	}
	f.bodies[plRunPathOf(55)] = plRunBody(55, "COMPLETED", "SUCCESSFUL", "2026-11-10T01:00:00Z", "2026-11-10T01:05:00Z", "main", "")
	evs, cur, hint, err = p.Poll(ctx, subj, cur, nil)
	if err != nil || types(evs) != EventRunCompleted || !hint.Settled || evs[0].Source != PipelinesKind {
		t.Fatalf("success: %v %v %+v", evs, err, hint)
	}
	if !strings.Contains(evs[0].Summary, "#55") || !strings.Contains(evs[0].Summary, "successful") {
		t.Errorf("summary %q", evs[0].Summary)
	}
	v1 := evs[0].Version

	// A failed run is completed and failed, names its failed steps in Data, and reads them once.
	f.bodies[plRunPathOf(55)] = plRunBody(55, "COMPLETED", "FAILED", "2026-11-10T01:00:00Z", "2026-11-10T02:05:00Z", "main", "")
	steps := "/2.0/repositories/w/r/pipelines/%7Bu-55%7D/steps/?pagelen=100"
	f.bodies[steps] = map[string]any{"values": []any{
		map[string]any{"name": "build", "state": map[string]any{"result": map[string]any{"name": "SUCCESSFUL"}}},
		map[string]any{"name": "test", "state": map[string]any{"result": map[string]any{"name": "FAILED"}}},
	}}
	evs, cur, _, err = p.Poll(ctx, subj, cur, nil)
	if err != nil || types(evs) != EventRunCompleted+","+EventRunFailed {
		t.Fatalf("failure: %v %v", evs, err)
	}
	if evs[0].Version == v1 {
		t.Error("a run finished again kept its version")
	}
	if len(evs[0].Data) != 2 || evs[0].Data[1].Text != "Failed step: test" {
		t.Errorf("data %+v", evs[0].Data)
	}
	if strings.Contains(evs[0].Summary, "test") {
		t.Errorf("third-party text in the summary: %q", evs[0].Summary)
	}
	f.served = map[string]int{}
	if _, _, _, err = p.Poll(ctx, subj, cur, nil); err != nil || f.served[steps] != 0 {
		t.Errorf("steps read again: %v %v", err, f.served)
	}

	// STOPPED (cancelled) counts as failed, as a cancelled Actions run does.
	f.bodies[plRunPathOf(56)] = plRunBody(56, "COMPLETED", "STOPPED", "2026-11-10T01:00:00Z", "2026-11-10T02:05:00Z", "main", "")
	f.bodies["/2.0/repositories/w/r/pipelines/%7Bu-56%7D/steps/?pagelen=100"] = map[string]any{"values": []any{}}
	if evs, _, _, err = p.Poll(ctx, "bitbucket:w/r/pipelines/runs/56", nil, nil); err != nil || types(evs) != EventRunCompleted+","+EventRunFailed {
		t.Fatalf("stopped: %v %v", evs, err)
	}

	// A deleted run is gone; a rate limit holds the source off until Retry-After.
	var gone *ErrGone
	if _, _, _, err = p.Poll(ctx, "bitbucket:w/r/pipelines/runs/99", nil, nil); !errors.As(err, &gone) {
		t.Fatalf("missing run: %v", err)
	}
	f.status[plRunPathOf(58)] = http.StatusTooManyRequests
	f.headers.Set("Retry-After", "120")
	p.Now = func() time.Time { return time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC) }
	var rl *ErrRateLimited
	if _, _, _, err = p.Poll(ctx, "bitbucket:w/r/pipelines/runs/58", nil, nil); !errors.As(err, &rl) || !rl.Until.Equal(p.Now().Add(2*time.Minute)) {
		t.Fatalf("rate limit: %v", err)
	}
}

func TestResolvePipelineFloorComesFromBitbucketsAnswer(t *testing.T) {
	f := newFakeGitHub()
	f.bodies[plList5] = map[string]any{"values": []any{
		plRunBody(31, "COMPLETED", "SUCCESSFUL", "2026-11-10T01:00:00Z", "2026-11-10T01:01:00Z", "dev", ""),
		plRunBody(30, "COMPLETED", "SUCCESSFUL", "2026-11-09T01:00:00Z", "2026-11-09T01:01:00Z", "main", "nightly"),
	}}
	p := newTestPipelines(t, f)
	floorUnix := time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC).Unix()
	res, err := p.Resolve(context.Background(), "w/r:pipelines/nightly@main", "")
	want := SubjectID("bitbucket:w/r/pipelines/after/31-" + strconv.FormatInt(floorUnix, 10) + "/name/nightly/branch/main")
	if err != nil || res.Subject != want || !TokenOK(string(res.Subject)) || !plPipSubjectRE.MatchString(string(res.Subject)) {
		t.Fatalf("%+v %v", res, err)
	}
	// A repository with no run yet takes Bitbucket's own Date header, to the minute.
	f.bodies[plList5] = map[string]any{"values": []any{}}
	f.headers.Set("Date", "Tue, 10 Nov 2026 03:04:56 GMT")
	res, err = p.Resolve(context.Background(), "w/r:pipelines", "")
	d := time.Date(2026, 11, 10, 3, 4, 0, 0, time.UTC).Unix()
	if err != nil || res.Subject != SubjectID("bitbucket:w/r/pipelines/after/0-"+strconv.FormatInt(d, 10)) {
		t.Fatalf("empty: %+v %v", res, err)
	}
	// An unreadable repository is refused.
	f.status[plList5] = http.StatusNotFound
	var ref *ErrRefused
	if _, err = p.Resolve(context.Background(), "w/r:pipelines", ""); !errors.As(err, &ref) {
		t.Fatalf("unreadable: %v", err)
	}
}

func TestPollPipelineOnlyToldForARunAfterTheFloor(t *testing.T) {
	f := newFakeGitHub()
	p := newTestPipelines(t, f)
	ctx := context.Background()
	floorUnix := time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC).Unix()
	subj := plPipSubject("w/r", "", "main", 31, floorUnix)
	f.bodies[plList50] = map[string]any{"values": []any{
		// Older than the floor by build number, though "completed later": never new.
		plRunBody(31, "COMPLETED", "SUCCESSFUL", "2026-11-10T01:00:00Z", "2026-11-10T03:00:00Z", "main", ""),
		plRunBody(30, "COMPLETED", "SUCCESSFUL", "2026-11-09T01:00:00Z", "2026-11-09T01:01:00Z", "main", ""),
	}}
	evs, cur, hint, err := p.Poll(ctx, subj, nil, nil)
	if err != nil || len(evs) != 0 || hint.After != plIdleAfter {
		t.Fatalf("old runs: %v %v %+v", evs, err, hint)
	}
	// A new run on another branch, then one on main that is still going.
	f.bodies[plList50] = map[string]any{"values": []any{
		plRunBody(33, "IN_PROGRESS", "", "2026-11-10T02:00:00Z", "", "main", ""),
		plRunBody(32, "COMPLETED", "SUCCESSFUL", "2026-11-10T01:30:00Z", "2026-11-10T01:40:00Z", "dev", ""),
		plRunBody(31, "COMPLETED", "SUCCESSFUL", "2026-11-10T01:00:00Z", "2026-11-10T03:00:00Z", "main", ""),
	}}
	evs, cur, hint, err = p.Poll(ctx, subj, cur, nil)
	if err != nil || len(evs) != 0 || hint.After != plBusyAfter {
		t.Fatalf("running: %v %v %+v", evs, err, hint)
	}
	f.bodies[plList50] = map[string]any{"values": []any{
		plRunBody(33, "COMPLETED", "FAILED", "2026-11-10T02:00:00Z", "2026-11-10T02:10:00Z", "main", ""),
		plRunBody(32, "COMPLETED", "SUCCESSFUL", "2026-11-10T01:30:00Z", "2026-11-10T01:40:00Z", "dev", ""),
	}}
	f.bodies["/2.0/repositories/w/r/pipelines/%7Bu-33%7D/steps/?pagelen=100"] = map[string]any{"values": []any{}}
	evs, _, _, err = p.Poll(ctx, subj, cur, nil)
	if err != nil || types(evs) != EventRunCompleted+","+EventRunFailed || !strings.HasPrefix(evs[0].Version, "33:") {
		t.Fatalf("new run: %v %v", evs, err)
	}
	// A watch for a custom pipeline ignores runs of another selector; one for any branch takes both.
	named := plPipSubject("w/r", "nightly", "", 31, floorUnix)
	if evs, _, _, err = p.Poll(ctx, named, nil, nil); err != nil || len(evs) != 0 {
		t.Fatalf("named: %v %v", evs, err)
	}
	any := plPipSubject("w/r", "", "", 31, floorUnix)
	if evs, _, _, err = p.Poll(ctx, any, nil, nil); err != nil || !strings.HasPrefix(evs[0].Version, "33:") {
		t.Fatalf("any: %v %v", evs, err)
	}
}

func TestPollPipelineFollowsNextLinksAndStopsAtTheFloor(t *testing.T) {
	f := newFakeGitHub()
	p := newTestPipelines(t, f)
	ctx := context.Background()
	floorUnix := time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC).Unix()
	subj := plPipSubject("w/r", "", "main", 10, floorUnix)
	var page1 []any
	for b := int64(70); b > 20; b-- { // 50 busy runs on another branch
		page1 = append(page1, plRunBody(b, "IN_PROGRESS", "", "2026-11-10T02:00:00Z", "", "dev", ""))
	}
	next := p.API + "/2.0/repositories/w/r/pipelines/?sort=-created_on&pagelen=50&page=2"
	f.bodies[plList50] = map[string]any{"values": page1, "next": next}
	f.bodies["/2.0/repositories/w/r/pipelines/?sort=-created_on&pagelen=50&page=2"] = map[string]any{"values": []any{
		plRunBody(15, "COMPLETED", "SUCCESSFUL", "2026-11-10T01:10:00Z", "2026-11-10T01:20:00Z", "main", ""),
		plRunBody(10, "COMPLETED", "SUCCESSFUL", "2026-11-09T01:10:00Z", "2026-11-09T01:20:00Z", "main", ""),
	}}
	evs, _, _, err := p.Poll(ctx, subj, nil, nil)
	if err != nil || len(evs) != 1 || !strings.HasPrefix(evs[0].Version, "15:") {
		t.Fatalf("paged: %v %v", evs, err)
	}
	// A next link off the API host is not followed (the token would go with it).
	f.bodies[plList50] = map[string]any{"values": page1, "next": "https://evil.example/x"}
	f.tokens = nil
	if evs, _, _, err = p.Poll(ctx, subj, nil, nil); err != nil || len(evs) != 0 {
		t.Fatalf("foreign next: %v %v", evs, err)
	}
	// A page that fails fails the whole read.
	f.bodies[plList50] = map[string]any{"values": page1, "next": next}
	f.status["/2.0/repositories/w/r/pipelines/?sort=-created_on&pagelen=50&page=2"] = http.StatusInternalServerError
	if _, _, _, err = p.Poll(ctx, subj, nil, nil); err == nil {
		t.Fatal("a failed later page was taken as an empty one")
	}
}

func TestPipelinesRenewAToken(t *testing.T) {
	f := newFakeGitHub()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer new" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	f.bodies[plRunPathOf(5)] = plRunBody(5, "IN_PROGRESS", "", "2026-11-10T01:00:00Z", "", "main", "")
	p := NewBitbucketPipelines(func() (string, error) { return "Bearer old", nil }, func(string) (string, error) { return "Bearer new", nil })
	p.API = srv.URL
	if _, err := p.Resolve(context.Background(), "w/r:run/5", ""); err != nil {
		t.Fatal(err)
	}
	p.Renew = func(r string) (string, error) { return r, nil } // a pasted API token cannot be renewed
	var ref *ErrRefused
	if _, err := p.Resolve(context.Background(), "w/r:run/5", ""); !errors.As(err, &ref) || !strings.Contains(ref.Reason, "reconnect") {
		t.Fatalf("rejected: %v", err)
	}
	p.Auth = func() (string, error) { return "", nil }
	if _, err := p.Resolve(context.Background(), "w/r:run/5", ""); !errors.As(err, &ref) || !strings.Contains(ref.Reason, "not connected") {
		t.Fatalf("not connected: %v", err)
	}
}

func TestPipelinesWatchLimit(t *testing.T) {
	var s Source = &BitbucketPipelines{}
	l, ok := s.(SourceLimiter)
	if !ok || l.MaxSubscriptions() != plMaxSubscriptions {
		t.Fatal("pipelines watches are not bounded")
	}
}

// One subscription works on both providers: the same types, the same notice shape.
func TestOneSubscriptionShapeWorksOnBothProviders(t *testing.T) {
	f := newFakeGitHub()
	f.bodies[plRunPathOf(55)] = plRunBody(55, "COMPLETED", "SUCCESSFUL", "2026-11-10T01:00:00Z", "2026-11-10T01:05:00Z", "main", "")
	f.bodies["/repos/o/r/actions/runs/55"] = run(55, "completed", "success", "2026-11-10T01:00:00Z", 1)
	pl := newTestPipelines(t, f)
	gh := newTestActions(t, f)
	sink := &fakeSink{answer: Ready}
	e, _, _ := newTestEngine(pl, sink)
	e.sources[ActionsKind] = gh
	ctx := context.Background()
	for _, r := range []Request{
		{Owner: "s", Source: PipelinesKind, Ref: "w/r:run/55", Types: []string{"ci.run.completed"}},
		{Owner: "t", Source: ActionsKind, Ref: "o/r:run/55", Types: []string{"ci.run.completed"}},
	} {
		if _, err := e.Subscribe(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	e.Tick(ctx)
	if len(sink.told) != 2 || !strings.HasPrefix(sink.told[0], "s:ci.run.completed:55:") || sink.told[1] != "t:ci.run.completed:55:1" {
		t.Fatalf("told %v", sink.told)
	}
	if !strings.Contains(sink.notices[0], "source=bitbucket_pipelines type=ci.run.completed subject=bitbucket:w/r/pipelines/runs/55") {
		t.Fatalf("notice %s", sink.notices[0])
	}
	_ = json.Marshal
}

func TestPipelinesAcceptTheLegacyTypeSpelling(t *testing.T) {
	al := (&BitbucketPipelines{}).TypeAliases()
	if got, _ := canonPattern("github.workflow_run.*", al); got != "ci.run.*" {
		t.Fatalf("got %s", got)
	}
}

// A redirect to another origin must not carry the token: not to another port, not to plain http.
func TestPipelinesDoNotSendTheTokenToAnotherOrigin(t *testing.T) {
	var leaked []string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = append(leaked, r.Header.Get("Authorization"))
	}))
	t.Cleanup(other.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/x", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	p := NewBitbucketPipelines(func() (string, error) { return "Bearer tok", nil }, nil)
	p.API = srv.URL
	if _, err := p.Resolve(context.Background(), "w/r:run/5", ""); err == nil {
		t.Fatal("a redirect answer was taken as a run")
	}
	if len(leaked) != 0 {
		t.Fatalf("token sent to another origin: %v", leaked)
	}
}

func TestOwnLinkIsCaseInsensitiveOnTheHostAndRefusesOthers(t *testing.T) {
	p := &BitbucketPipelines{API: "https://api.bitbucket.org"}
	for link, want := range map[string]bool{
		"https://api.bitbucket.org/2.0/x?page=2":      true,
		"https://API.Bitbucket.org/2.0/x?page=2":      true,
		"http://api.bitbucket.org/2.0/x":              false,
		"https://api.bitbucket.org:8443/2.0/x":        false,
		"https://u:p@api.bitbucket.org/2.0/x":         false,
		"https://api.bitbucket.org.evil.example/2.0/": false,
		"": false,
	} {
		if (p.ownLink(link) != "") != want {
			t.Errorf("%q: want %v", link, want)
		}
	}
}
