// workitems_test.go — the work-item inbox (docs/log/80 / ADR 0061).
//
// What is pinned here is the four things that hurt most when they break:
//   - a stopped workspace still serves the cache and fetches nothing (showing the rail
//     must not wake a Workspace)
//   - when one query fails, only that query's rows stay stale; the others still refresh
//   - a fetch that never reached the Agent does not advance fetched_at (advancing it goes
//     quiet for five minutes)
//   - "last fetched" is the oldest stamp among the enabled queries
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

type workItemEnv struct {
	api  workItemsAPI
	res  *resolved
	st   *store.SQL
	mid  string
	body func() string // what the stub Agent answers /work-items/fetch with
	hits *int
	sent *string // the last request body the stub Agent received
}

func newWorkItemEnv(t *testing.T, state string) *workItemEnv {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	id, _ := st.UpsertIdentity(ctx, "wi@example.com", "wi", "")
	m, _ := st.EnsureMembership(ctx, id.ID, tenant.ID, "member")

	env := &workItemEnv{st: st, mid: m.ID, hits: new(int), sent: new(string)}
	env.body = func() string { return `{"items":[],"errors":[]}` }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/work-items/fetch" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		*env.hits++
		b, _ := io.ReadAll(r.Body)
		*env.sent = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(env.body()))
	}))
	t.Cleanup(srv.Close)

	mgr := &manager{store: st}
	env.api = workItemsAPI{memberAuth{mgr}, st}
	env.res = &resolved{rt: stubRuntime{endpoint: srv.URL, token: "tok", state: state},
		mv: store.MembershipView{MembershipID: m.ID}}
	return env
}

func (e *workItemEnv) addQuery(t *testing.T, id, label, query string, enabled bool) {
	t.Helper()
	q := store.WorkItemQuery{ID: id, MembershipID: e.mid, Provider: "github", Label: label,
		Query: query, Enabled: enabled, CreatedAt: store.NowTS()}
	if err := e.st.CreateWorkItemQuery(context.Background(), q); err != nil {
		t.Fatal(err)
	}
}

// A stopped workspace does not fetch; it serves the cache as it is. The most valuable screen
// this feature has is "read your tickets without waking a stopped Workspace", so if this
// breaks the core of the design goes with it (ADR 0061 decision 1).
func TestWorkItemsStoppedServesCacheWithoutFetching(t *testing.T) {
	env := newWorkItemEnv(t, "stopped")
	ctx := context.Background()
	env.addQuery(t, "q1", "自分の未完了", "assignee:@me is:open", true)
	if err := env.st.ReplaceWorkItems(ctx, env.mid, []string{"q1"}, []store.WorkItem{{
		ID: store.NewID(), MembershipID: env.mid, QueryID: "q1", Provider: "github", Kind: "issue",
		Key: "acme/web#45", Title: "古い行", State: "open", FetchedAt: "2026-08-25T00:00:00Z",
	}}); err != nil {
		t.Fatal(err)
	}
	out, aerr := env.api.workItemsPayload(ctx, env.res, "stopped")
	if aerr != nil {
		t.Fatalf("payload: %v", aerr)
	}
	if got := out.Running; got != false {
		t.Errorf("running = %v, want false", got)
	}
	if items := out.Items; len(items) != 1 || items[0].Key != "acme/web#45" {
		t.Errorf("stopped workspace lost the cache: %+v", out.Items)
	}
	if *env.hits != 0 {
		t.Errorf("fetched %d times while stopped — the rail must never wake the workspace", *env.hits)
	}
}

// A failure in one query replaces only the rows of the queries that succeeded. The failed
// query's rows stay, because dropping them empties the whole shelf over a single 401.
func TestWorkItemsPartialFailureKeepsOtherRows(t *testing.T) {
	env := newWorkItemEnv(t, "running")
	ctx := context.Background()
	env.addQuery(t, "ok", "動く方", "assignee:@me", true)
	env.addQuery(t, "ng", "壊れた方", "bad query", true)
	if err := env.st.ReplaceWorkItems(ctx, env.mid, []string{"ng"}, []store.WorkItem{{
		ID: store.NewID(), MembershipID: env.mid, QueryID: "ng", Provider: "github",
		Key: "acme/web#1", Title: "壊れたクエリの古い行", State: "open", FetchedAt: "2026-08-25T00:00:00Z",
	}}); err != nil {
		t.Fatal(err)
	}
	env.body = func() string {
		return `{"items":[{"queryId":"ok","provider":"github","kind":"issue","key":"acme/web#9",
		          "title":"新しい行","state":"open","url":"https://example.invalid/9",
		          "labels":["bug"],"labelColors":{"bug":"d73a4a"},
		          "repo":"acme/web","updatedAt":"2026-08-26T00:00:00Z",
		          "checks":{"state":"failure","total":40,"failed":3,"pending":1},"mergeable":"conflict",
		          "type":"Bug"}],
		         "errors":[{"queryId":"ng","message":"github could not parse the query"}]}`
	}
	env.api.refreshNow(ctx, env.res, true)

	items, err := env.st.ListWorkItems(ctx, env.mid)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("want both the new row and the failed query's old row, got %d: %+v", len(items), items)
	}
	// The Agent's label colours survive the cache and reach the wire.
	for _, it := range items {
		if it.Key == "acme/web#9" {
			dto := workItemToDTO(it)
			if got := dto.LabelColors["bug"]; got != "d73a4a" {
				t.Errorf("bug colour through the cache = %q, want d73a4a", got)
			}
			// So do the pull request's CI and conflict status (#1113).
			want := workItemChecksDTO{State: "failure", Total: 40, Failed: 3, Pending: 1}
			if dto.Checks != want || dto.Mergeable != "conflict" {
				t.Errorf("status through the cache = %+v / %q, want %+v / conflict", dto.Checks, dto.Mergeable, want)
			}
			// And the issue type the branch-name resolver maps to a kind (#1125).
			if dto.Type != "Bug" {
				t.Errorf("type through the cache = %q, want Bug", dto.Type)
			}
		} else if dto := workItemToDTO(it); dto.Checks != (workItemChecksDTO{}) || dto.Mergeable != "" {
			// A row that never carried a status (here: cached before the refresh) reads as not read.
			t.Errorf("old row status = %+v / %q, want zero", dto.Checks, dto.Mergeable)
		}
	}
	queries, _ := env.st.ListWorkItemQueries(ctx, env.mid)
	for _, q := range queries {
		switch q.ID {
		case "ok":
			if q.LastError != "" || q.FetchedAt == "" {
				t.Errorf("ok query: err=%q fetched=%q", q.LastError, q.FetchedAt)
			}
		case "ng":
			if q.LastError == "" {
				t.Error("failed query kept no error — the rail has nothing to show")
			}
			if q.FetchedAt == "" {
				t.Error("failed query must still stamp fetched_at (it is the rate limiter)")
			}
		}
	}
}

// #2709: the Agent's canComment flag survives the cache and reaches the DTO; a row from an Agent
// that sends none (every row before this field) stays false, so the Console keeps it read-only.
func TestWorkItemsCanCommentThroughTheCache(t *testing.T) {
	env := newWorkItemEnv(t, "running")
	ctx := context.Background()
	env.addQuery(t, "bb", "Bitbucket", "acme/web", true)
	env.body = func() string {
		return `{"items":[
		  {"queryId":"bb","provider":"bitbucket","kind":"pr","key":"acme/web#1","title":"a","state":"open","labels":[],"labelColors":{},"canComment":true},
		  {"queryId":"bb","provider":"bitbucket","kind":"pr","key":"acme/web#2","title":"b","state":"open","labels":[],"labelColors":{},"canComment":false},
		  {"queryId":"bb","provider":"bitbucket","kind":"pr","key":"acme/web#3","title":"c","state":"open","labels":[],"labelColors":{}}],
		 "errors":[]}`
	}
	env.api.refreshNow(ctx, env.res, true)
	items, err := env.st.ListWorkItems(ctx, env.mid)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, it := range items {
		got[it.Key] = workItemToDTO(it).CanComment
	}
	want := map[string]bool{"acme/web#1": true, "acme/web#2": false, "acme/web#3": false}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s canComment = %v, want %v (all: %v)", k, got[k], v, got)
		}
	}
}

// #2709: a cached true must not outlive the evidence for it. A failed read, a reconnect or a
// disconnect withdraws it (rows stay), and a fetch that began before the connection changed cannot
// bring it back.
func TestWorkItemsCanCommentIsWithdrawnWhenTheConnectionChanges(t *testing.T) {
	env := newWorkItemEnv(t, "running")
	ctx := context.Background()
	env.addQuery(t, "bb", "Bitbucket", "acme/web", true)
	granted := `{"items":[{"queryId":"bb","provider":"bitbucket","kind":"pr","key":"acme/web#1","title":"a","state":"open","labels":[],"labelColors":{},"canComment":true}],"errors":[]}`
	env.body = func() string { return granted }
	can := func() bool {
		t.Helper()
		items, err := env.st.ListWorkItems(ctx, env.mid)
		if err != nil || len(items) != 1 {
			t.Fatalf("items=%v err=%v", items, err)
		}
		return workItemToDTO(items[0]).CanComment
	}
	env.api.refreshNow(ctx, env.res, true)
	if !can() {
		t.Fatal("setup: the granted row must be commentable")
	}

	// 1. The next read fails (not connected, no access): the kept row loses the capability.
	env.body = func() string {
		return `{"items":[],"errors":[{"queryId":"bb","message":"Bitbucket is not connected"}]}`
	}
	env.api.refreshNow(ctx, env.res, true)
	if can() {
		t.Error("a failed query kept its stale canComment=true")
	}

	// 2. A reconnect/disconnect withdraws it from rows that are otherwise cached fine.
	env.body = func() string { return granted }
	env.api.refreshNow(ctx, env.res, true)
	if !can() {
		t.Fatal("setup: regranted")
	}
	resetWorkItemCommentCapability(ctx, env.st, env.mid)
	if can() {
		t.Error("a connection change left canComment=true in the cache")
	}

	// 3. A fetch that was already in flight when the connection changed carries the OLD scopes.
	env.body = func() string {
		resetWorkItemCommentCapability(ctx, env.st, env.mid) // the reconnect lands during the fetch
		return granted
	}
	env.api.refreshNow(ctx, env.res, true)
	if can() {
		t.Error("an in-flight fetch from before the reconnect restored canComment=true")
	}
	// The row itself is still listed.
	if items, _ := env.st.ListWorkItems(ctx, env.mid); len(items) != 1 {
		t.Errorf("rows were dropped: %d", len(items))
	}
}

// racingStore runs `before` right at the entrance of the commit, i.e. after the caller's own checks
// and before its rows land: the window an in-process comparison cannot see.
type racingStore struct {
	store.WorkItemStore
	before func()
}

func (r racingStore) ReplaceWorkItemsAt(ctx context.Context, mid string, qids []string, items []store.WorkItem, gen int64) error {
	r.before()
	return r.WorkItemStore.ReplaceWorkItemsAt(ctx, mid, qids, items, gen)
}

// The whole connection change (before-reset, new token, after-reset) completes between the fetch's
// last check and its write; the old connection's true must still not land.
func TestWorkItemsCanCommentCannotBeRestoredByACommitRace(t *testing.T) {
	env := newWorkItemEnv(t, "running")
	ctx := context.Background()
	env.addQuery(t, "bb", "Bitbucket", "acme/web", true)
	env.body = func() string {
		return `{"items":[{"queryId":"bb","provider":"bitbucket","kind":"pr","key":"acme/web#1","title":"a","state":"open","labels":[],"labelColors":{},"canComment":true}],"errors":[]}`
	}
	env.api.store = racingStore{env.st, func() {
		resetWorkItemCommentCapability(ctx, env.st, env.mid)
		resetWorkItemCommentCapability(ctx, env.st, env.mid)
	}}
	env.api.refreshNow(ctx, env.res, true)
	items, err := env.st.ListWorkItems(ctx, env.mid)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%v err=%v", items, err)
	}
	if items[0].CanComment {
		t.Error("a fetch from before the reconnect restored canComment=true after both resets")
	}
}

// #1095: a query whose page left matches out says so, and keeps saying so while the Workspace is
// stopped. Three cases in one refresh — a known count, a full page without a count, and a query
// that fit — then a refresh that fails, which must keep the note next to the rows it kept.
func TestWorkItemsMatchTotalThroughTheCache(t *testing.T) {
	env := newWorkItemEnv(t, "running")
	ctx := context.Background()
	env.addQuery(t, "gh", "GitHub", "is:open involves:@me", true)
	env.addQuery(t, "jr", "Jira", "assignee = currentUser()", true)
	env.addQuery(t, "fit", "Fits", "is:open repo:acme/web", true)
	env.body = func() string {
		return `{"items":[],"errors":[],
		         "truncated":[{"queryId":"gh","total":159},{"queryId":"jr","total":0}]}`
	}
	env.api.refreshNow(ctx, env.res, true)

	want := map[string]int{"gh": 159, "jr": -1, "fit": 0}
	check := func(when string) {
		t.Helper()
		out, aerr := env.api.workItemsPayload(ctx, env.res, "stopped")
		if aerr != nil {
			t.Fatalf("payload: %v", aerr)
		}
		for _, q := range out.Queries {
			if q.MatchTotal != want[q.ID] {
				t.Errorf("%s: %s matchTotal = %d, want %d", when, q.ID, q.MatchTotal, want[q.ID])
			}
		}
	}
	check("after the fetch")

	env.body = func() string {
		return `{"items":[],"errors":[{"queryId":"gh","message":"github rate limit reached"}],"truncated":[]}`
	}
	env.api.refreshNow(ctx, env.res, true)
	want["jr"] = 0 // fetched fine this time and nothing was left out
	check("after a failed refresh")
}

// When the request never reached the Agent (just stopped, restarting) fetched_at is left
// alone: advancing it would count "it did not arrive" as fetched and stay silent for the
// five minutes of the interval.
func TestWorkItemsUnreachableAgentKeepsStamp(t *testing.T) {
	env := newWorkItemEnv(t, "running")
	ctx := context.Background()
	env.addQuery(t, "q1", "自分の未完了", "assignee:@me", true)
	if err := env.st.MarkWorkItemQueryFetched(ctx, "q1", "2026-08-25T00:00:00Z", ""); err != nil {
		t.Fatal(err)
	}
	env.res.rt = stubRuntime{endpoint: "http://127.0.0.1:1", token: "tok", state: "running"}
	env.api.refreshNow(ctx, env.res, true)

	q, ok, err := env.st.GetWorkItemQuery(ctx, "q1")
	if err != nil || !ok {
		t.Fatalf("get: %v %v", err, ok)
	}
	if q.FetchedAt != "2026-08-25T00:00:00Z" {
		t.Errorf("fetched_at advanced to %q on a transport failure", q.FetchedAt)
	}
	if q.LastError == "" {
		t.Error("transport failure left no error on the query")
	}
}

// "Last fetched" is the oldest stamp among the enabled queries. Showing the newest would
// claim a list half of which is stale was fetched just now.
func TestWorkItemsFetchedAtIsOldestEnabled(t *testing.T) {
	env := newWorkItemEnv(t, "stopped")
	ctx := context.Background()
	env.addQuery(t, "new", "新しい", "a", true)
	env.addQuery(t, "old", "古い", "b", true)
	env.addQuery(t, "off", "無効", "c", false)
	_ = env.st.MarkWorkItemQueryFetched(ctx, "new", "2026-08-26T10:00:00Z", "")
	_ = env.st.MarkWorkItemQueryFetched(ctx, "old", "2026-08-26T09:00:00Z", "")
	_ = env.st.MarkWorkItemQueryFetched(ctx, "off", "2020-01-01T00:00:00Z", "")

	out, aerr := env.api.workItemsPayload(ctx, env.res, "stopped")
	if aerr != nil {
		t.Fatal(aerr)
	}
	if got := out.FetchedAt; got != "2026-08-26T09:00:00Z" {
		t.Errorf("fetchedAt = %v, want the oldest ENABLED query's stamp", got)
	}
}

// The fetch interval (5 minutes). The SSE tick is 4 seconds, so without it GitHub is hit
// continuously, once per open tab.
func TestWorkItemsRefreshIntervalThrottles(t *testing.T) {
	env := newWorkItemEnv(t, "running")
	ctx := context.Background()
	env.addQuery(t, "q1", "自分の未完了", "assignee:@me", true)
	_ = env.st.MarkWorkItemQueryFetched(ctx, "q1", store.NowTS(), "")

	env.api.refreshNow(ctx, env.res, false)
	if *env.hits != 0 {
		t.Errorf("fetched %d times inside the interval", *env.hits)
	}
	// Once the stamp is stale it goes through.
	stale := time.Now().UTC().Add(-2 * workItemsRefreshEvery).Format(time.RFC3339)
	_ = env.st.MarkWorkItemQueryFetched(ctx, "q1", stale, "")
	env.api.refreshNow(ctx, env.res, false)
	if *env.hits != 1 {
		t.Errorf("hits = %d, want 1 once the stamp went stale", *env.hits)
	}
	// force ignores the interval (the refresh button).
	env.api.refreshNow(ctx, env.res, true)
	if *env.hits != 2 {
		t.Errorf("hits = %d, want the forced refresh to ignore the interval", *env.hits)
	}
}

// A disabled query drops out of the fetch set: its rows stay, but nothing is called for it.
func TestWorkItemsDisabledQueryNotFetched(t *testing.T) {
	env := newWorkItemEnv(t, "running")
	env.addQuery(t, "q1", "無効", "assignee:@me", false)
	env.api.refreshNow(context.Background(), env.res, true)
	if *env.hits != 0 {
		t.Errorf("a disabled query was fetched %d times", *env.hits)
	}
}

func TestWorkItemQueryValidation(t *testing.T) {
	mv := store.MembershipView{MembershipID: "m1"}
	// jira is accepted; an unknown provider is still refused, because being turned down on
	// the spot beats being stored as a row that can never fetch anything.
	if _, aerr := validateWorkItemQuery(mv, workItemQueryDTO{Provider: "jira", Query: "assignee = currentUser()"}); aerr != nil {
		t.Errorf("jira must be accepted since P1: %v", aerr)
	}
	if _, aerr := validateWorkItemQuery(mv, workItemQueryDTO{Provider: "backlog", Query: "x"}); aerr == nil {
		t.Error("an unknown provider must be refused rather than saved as a row that can never fetch")
	}
	if _, aerr := validateWorkItemQuery(mv, workItemQueryDTO{Query: "   "}); aerr == nil {
		t.Error("empty query accepted")
	}
	q, aerr := validateWorkItemQuery(mv, workItemQueryDTO{Query: " assignee:@me is:open "})
	if aerr != nil {
		t.Fatalf("valid query refused: %v", aerr)
	}
	if q.Provider != "github" {
		t.Errorf("provider default = %q, want github", q.Provider)
	}
	if q.Label != "assignee:@me is:open" {
		t.Errorf("label should fall back to the query, got %q", q.Label)
	}
}

// The ledger is idempotent on (item, session): a retried launch does not add a second row.
func TestWorkItemSessionLedgerIdempotent(t *testing.T) {
	env := newWorkItemEnv(t, "running")
	post := func() *httptest.ResponseRecorder {
		body := `{"provider":"github","itemKey":"acme/web#45","sessionName":"sk7f3q9","repo":"web","branch":"feature/web-45"}`
		r := httptest.NewRequest("POST", "/api/work-item-sessions", strings.NewReader(body))
		w := httptest.NewRecorder()
		env.api.createSession(w, r, store.Identity{}, store.MembershipView{MembershipID: env.mid})
		return w
	}
	first, second := post(), post()
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("codes = %d/%d", first.Code, second.Code)
	}
	var a, b workItemSessionDTO
	_ = json.Unmarshal(first.Body.Bytes(), &a)
	_ = json.Unmarshal(second.Body.Bytes(), &b)
	if a.ID != b.ID {
		t.Errorf("retried launch created a second ledger row (%s vs %s)", a.ID, b.ID)
	}
	rows, _ := env.st.ListWorkItemSessions(context.Background(), env.mid)
	if len(rows) != 1 {
		t.Errorf("ledger rows = %d, want 1", len(rows))
	}
}

// Deleting a query removes only the cache; the ledger — the record that work was started —
// survives.
func TestDeleteQueryKeepsLedger(t *testing.T) {
	env := newWorkItemEnv(t, "running")
	ctx := context.Background()
	env.addQuery(t, "q1", "自分の未完了", "assignee:@me", true)
	_ = env.st.ReplaceWorkItems(ctx, env.mid, []string{"q1"}, []store.WorkItem{{
		ID: store.NewID(), MembershipID: env.mid, QueryID: "q1", Key: "acme/web#45", FetchedAt: store.NowTS(),
	}})
	_ = env.st.CreateWorkItemSession(ctx, store.WorkItemSession{ID: store.NewID(), MembershipID: env.mid,
		Provider: "github", ItemKey: "acme/web#45", SessionName: "sk7f3q9", CreatedAt: store.NowTS()})

	if err := env.st.DeleteWorkItemQuery(ctx, "q1", env.mid); err != nil {
		t.Fatal(err)
	}
	if items, _ := env.st.ListWorkItems(ctx, env.mid); len(items) != 0 {
		t.Errorf("cache rows survived the query delete: %d", len(items))
	}
	rows, _ := env.st.ListWorkItemSessions(ctx, env.mid)
	if len(rows) != 1 {
		t.Errorf("ledger rows = %d, want the started-work record to outlive the query", len(rows))
	}
}

// A nil Go slice marshals as JSON null, and the Console's item.labels.slice(...) then throws
// a TypeError; with no ErrorBoundary in the app it is the whole Console that goes blank, not
// just the section. A single issue with no labels is enough to trigger it, so the wire is
// pinned never to carry a null array.
// The live pull request read (docs/log/80 §80.24) is a look, not a fetch: a stopped Workspace
// answers 409 and is NOT started for it, and nothing reaches the cache either way. Both halves
// matter — starting a workspace to render a panel is the same hole ADR 0061 decision 1 closed
// for the list, and writing the answer into work_item_cache would put a panel's read into the
// rail's history.
func TestWorkItemDetailStoppedDoesNotStartOrStore(t *testing.T) {
	env := newWorkItemEnv(t, "stopped")
	env.addQuery(t, "q1", "自分の PR", "is:open involves:@me", true)
	req := httptest.NewRequest("POST", "/api/work-items/detail",
		strings.NewReader(`{"provider":"github","key":"acme/web#518"}`))
	w := httptest.NewRecorder()
	env.api.detail(w, req, env.res)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (a panel must not wake a workspace)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "workspace_stopped") {
		t.Errorf("body = %s, want the workspace_stopped code the Console branches on", w.Body.String())
	}
	if *env.hits != 0 {
		t.Errorf("reached the agent %d times while stopped", *env.hits)
	}
	items, err := env.st.ListWorkItems(context.Background(), env.mid)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Errorf("the detail read wrote %d rows into the cache", len(items))
	}
}

// A key is the only coordinate this can work from, so an empty one is a 400 here rather than a
// round trip that ends as a provider error.
func TestWorkItemDetailNeedsAKey(t *testing.T) {
	env := newWorkItemEnv(t, "running")
	req := httptest.NewRequest("POST", "/api/work-items/detail", strings.NewReader(`{"provider":"github"}`))
	w := httptest.NewRecorder()
	env.api.detail(w, req, env.res)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// An Agent from before this feature answers 404 on the route. Passing that through would read as
// "the pull request is gone"; it has to say the workspace needs restarting instead.
func TestWorkItemDetailOldAgentSaysSo(t *testing.T) {
	env := newWorkItemEnv(t, "running") // the stub agent only serves /work-items/fetch
	req := httptest.NewRequest("POST", "/api/work-items/detail",
		strings.NewReader(`{"provider":"github","key":"acme/web#518"}`))
	w := httptest.NewRecorder()
	env.api.detail(w, req, env.res)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "agent_outdated") {
		t.Errorf("body = %s, want agent_outdated", w.Body.String())
	}
}

// The Agent decides issue vs. pull request from `kind`, so the relay has to carry it through:
// empty for a reference outside the inbox, "pr" for a cached row (#1697).
func TestWorkItemDetailRelaysKind(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`{"provider":"github","key":"acme/web#7"}`, ""},
		{`{"provider":"github","key":"acme/web#7","kind":"pr"}`, "pr"},
	} {
		env := newWorkItemEnv(t, "running")
		var got struct{ Kind string }
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&got)
			_, _ = w.Write([]byte(`{"key":"acme/web#7"}`))
		}))
		env.res = &resolved{rt: stubRuntime{endpoint: srv.URL, token: "tok", state: "running"}, mv: env.res.mv}
		req := httptest.NewRequest("POST", "/api/work-items/detail", strings.NewReader(tc.in))
		w := httptest.NewRecorder()
		env.api.detail(w, req, env.res)
		srv.Close()
		if w.Code != http.StatusOK || got.Kind != tc.want {
			t.Errorf("%s → status %d, agent saw kind %q, want %q", tc.in, w.Code, got.Kind, tc.want)
		}
	}
}

// An unknown kind is refused here, before the Agent is reached.
func TestWorkItemDetailRejectsUnknownKind(t *testing.T) {
	env := newWorkItemEnv(t, "running")
	req := httptest.NewRequest("POST", "/api/work-items/detail",
		strings.NewReader(`{"provider":"github","key":"acme/web#7","kind":"banana"}`))
	w := httptest.NewRecorder()
	env.api.detail(w, req, env.res)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if *env.hits != 0 {
		t.Errorf("reached the agent %d times", *env.hits)
	}
}

// The merge relay (ADR 0061, 2026-10-25 note on #2632).

// mergeRes is a resolved workspace owned by the env's member, whose Agent is srv.
func mergeRes(env *workItemEnv, endpoint, state string) *resolved {
	return &resolved{
		rt:    stubRuntime{endpoint: endpoint, token: "tok", state: state},
		ws:    store.Workspace{ID: "ws1", MembershipID: env.mid},
		ident: store.Identity{ID: "user1"},
		mv:    store.MembershipView{MembershipID: env.mid},
	}
}

// consoleMergeReq is the request the Console itself sends: JSON, no cross-site markers.
func consoleMergeReq(body string) *http.Request {
	r := httptest.NewRequest("POST", "/api/work-items/merge", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

const mergeBody = `{"provider":"github","key":"acme/web#518","sha":"abc","method":"merge"}`

// A stopped Workspace answers 409 and is not started, and nothing reaches the Agent.
func TestWorkItemMergeStoppedDoesNotStart(t *testing.T) {
	env := newWorkItemEnv(t, "stopped")
	var reached int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++ }))
	defer srv.Close()
	w := httptest.NewRecorder()
	env.api.merge(w, consoleMergeReq(mergeBody), mergeRes(env, srv.URL, "stopped"))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "workspace_stopped") {
		t.Fatalf("got %d %s, want 409 workspace_stopped", w.Code, w.Body.String())
	}
	if reached != 0 {
		t.Errorf("reached the agent %d times while stopped", reached)
	}
}

func TestWorkItemMergeRefusesLocally(t *testing.T) {
	for name, body := range map[string]string{
		"bitbucket": `{"provider":"bitbucket","key":"ws/r#1","sha":"abc","method":"merge"}`,
		"jira":      `{"provider":"jira","key":"PROJ-1","sha":"abc","method":"merge"}`,
		"no sha":    `{"provider":"github","key":"acme/web#1","method":"merge"}`,
		"no key":    `{"provider":"github","sha":"abc","method":"merge"}`,
		"no method": `{"provider":"github","key":"acme/web#1","sha":"abc"}`,
		"bad":       `{"provider":"github","key":"acme/web#1","sha":"abc","method":"ff"}`,
		"not json":  `x`,
	} {
		env := newWorkItemEnv(t, "running")
		w := httptest.NewRecorder()
		env.api.merge(w, consoleMergeReq(body), mergeRes(env, "http://127.0.0.1:1", "running"))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d %s, want 400", name, w.Code, w.Body.String())
		}
	}
}

// The Agent receives exactly the four fields (provider pinned to github) under a member-origin
// mark that covers that exact body, and its answer (here a moved head) passes through with its
// status and code intact.
func TestWorkItemMergeRelaysSignedAndPassesRefusalThrough(t *testing.T) {
	env := newWorkItemEnv(t, "running")
	var raw []byte
	var path, auth, mark string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth, mark = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get(memberOriginHeader)
		raw, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"head_changed","message":"moved"}}`))
	}))
	defer srv.Close()
	w := httptest.NewRecorder()
	env.api.merge(w, consoleMergeReq(`{"key":"acme/web#518","sha":"abc","method":"squash","extra":"x"}`), mergeRes(env, srv.URL, "running"))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "head_changed") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if path != "/work-items/merge" || auth != "Bearer tok" {
		t.Errorf("agent saw %s with %q", path, auth)
	}
	var got map[string]string
	_ = json.Unmarshal(raw, &got)
	if got["provider"] != "github" || got["key"] != "acme/web#518" || got["sha"] != "abc" || got["method"] != "squash" || len(got) != 4 {
		t.Errorf("agent body = %s", raw)
	}
	pub := memberOriginKey(env.api.mgr.tokenSignMaster(), memberOriginKeyVersion()).Public().(ed25519.PublicKey)
	c := decodeMark(t, pub, mark)
	sum := sha256.Sum256(raw)
	if c.Method != "POST" || c.Path != "/work-items/merge" || c.Query != "" || c.BodySHA256 != hex.EncodeToString(sum[:]) ||
		c.Membership != env.mid || c.Workspace != "ws1" || c.Subject != "user1" {
		t.Errorf("claims = %+v — the mark must cover the body that was sent, for this member", c)
	}
}

// A page other than the Console could have made this request with the member's cookie (a
// same-site preview app, text/plain no-cors): it is refused before anything is read, signed or
// sent. The Console's own JSON request still goes through.
func TestWorkItemMergeRefusesCrossOriginRequests(t *testing.T) {
	for name, mod := range map[string]func(*http.Request){
		"text/plain":      func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
		"no content type": func(r *http.Request) { r.Header.Del("Content-Type") },
		"same-site":       func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") },
		"cross-site":      func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"foreign origin":  func(r *http.Request) { r.Header.Set("Origin", "https://preview.example.test") },
	} {
		env := newWorkItemEnv(t, "running")
		var reached int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++ }))
		r := consoleMergeReq(mergeBody)
		mod(r)
		w := httptest.NewRecorder()
		env.api.merge(w, r, mergeRes(env, srv.URL, "running"))
		srv.Close()
		if w.Code != http.StatusUnsupportedMediaType && w.Code != http.StatusForbidden {
			t.Errorf("%s: got %d %s, want 415/403", name, w.Code, w.Body.String())
		}
		if reached != 0 {
			t.Errorf("%s: the Agent was reached", name)
		}
	}
	env := newWorkItemEnv(t, "running")
	var reached int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		_, _ = w.Write([]byte(`{"merged":true,"sha":"f"}`))
	}))
	defer srv.Close()
	r := consoleMergeReq(mergeBody)
	r.Host = "console.example.test"
	r.Header.Set("Origin", "https://console.example.test")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	env.api.merge(w, r, mergeRes(env, srv.URL, "running"))
	if w.Code != http.StatusOK || reached != 1 {
		t.Fatalf("the Console's own request: %d %s (agent reached %d)", w.Code, w.Body.String(), reached)
	}
}

// The mark speaks for the workspace's own member only.
func TestWorkItemMergeRefusesAnotherMembersWorkspace(t *testing.T) {
	env := newWorkItemEnv(t, "running")
	var reached int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++ }))
	defer srv.Close()
	res := mergeRes(env, srv.URL, "running")
	res.ws.MembershipID = "someone-else"
	w := httptest.NewRecorder()
	env.api.merge(w, consoleMergeReq(mergeBody), res)
	if w.Code != http.StatusForbidden || reached != 0 {
		t.Fatalf("got %d %s (agent reached %d)", w.Code, w.Body.String(), reached)
	}
}

// An Agent from before this feature has no route; that must not read as "pull request gone".
func TestWorkItemMergeOldAgentSaysSo(t *testing.T) {
	env := newWorkItemEnv(t, "running") // the stub agent serves /work-items/fetch only
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer srv.Close()
	w := httptest.NewRecorder()
	env.api.merge(w, consoleMergeReq(mergeBody), mergeRes(env, srv.URL, "running"))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "agent_outdated") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

func TestWorkItemWireNeverCarriesNullArrays(t *testing.T) {
	if got := splitLabels(""); got == nil {
		t.Error("splitLabels(\"\") returned nil — it marshals as JSON null")
	}
	if got := splitLabels("   "); got == nil {
		t.Error("splitLabels(blank) returned nil")
	}
	dto := workItemToDTO(store.WorkItem{ID: "1", Key: "PROJ-1", Labels: ""})
	enc, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(enc), `"labels":null`) {
		t.Fatalf("row wire carries a null array: %s", enc)
	}
	if !strings.Contains(string(enc), `"labels":[]`) {
		t.Errorf("want an empty array, got: %s", enc)
	}
	// A row cached before label_colors existed, or with an unreadable value, still carries {}.
	for _, stored := range []string{"", "not json", "null"} {
		enc, _ := json.Marshal(workItemToDTO(store.WorkItem{ID: "1", LabelColors: stored}))
		if !strings.Contains(string(enc), `"labelColors":{}`) {
			t.Errorf("label_colors %q: want labelColors {}, got: %s", stored, enc)
		}
	}
	// Likewise checks: a row cached before the column existed, or an unreadable value, is an
	// object with an empty state — never absent, never null.
	for _, stored := range []string{"", "not json", "null"} {
		enc, _ := json.Marshal(workItemToDTO(store.WorkItem{ID: "1", Checks: stored}))
		if !strings.Contains(string(enc), `"checks":{"state":"","total":0,"failed":0,"pending":0}`) {
			t.Errorf("checks %q: want an empty checks object, got: %s", stored, enc)
		}
	}
	if encodeWorkItemChecks(workItemChecksDTO{}) != "" {
		t.Error("no checks must store as '' so an issue row costs nothing")
	}
}

// #2779: the comment count survives the cache and reaches the DTO, and a row whose Agent sent none
// (Jira, an older Agent) stays unknown (null) rather than becoming a known 0. A refresh from an
// older Agent replaces a known count with unknown, never with 0.
func TestWorkItemsCommentsThroughTheCache(t *testing.T) {
	env := newWorkItemEnv(t, "running")
	ctx := context.Background()
	env.addQuery(t, "gh", "GitHub", "acme/web", true)
	env.body = func() string {
		return `{"items":[
		  {"queryId":"gh","provider":"github","kind":"issue","key":"acme/web#1","title":"a","state":"open","labels":[],"labelColors":{},"commentCount":7},
		  {"queryId":"gh","provider":"github","kind":"issue","key":"acme/web#2","title":"b","state":"open","labels":[],"labelColors":{}},
		  {"queryId":"gh","provider":"github","kind":"issue","key":"acme/web#3","title":"c","state":"open","labels":[],"labelColors":{},"commentCount":0}],
		 "errors":[]}`
	}
	read := func() map[string]*int {
		env.api.refreshNow(ctx, env.res, true)
		items, err := env.st.ListWorkItems(ctx, env.mid)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]*int{}
		for _, it := range items {
			got[it.Key] = workItemToDTO(it).CommentCount
		}
		return got
	}
	got := read()
	if got["acme/web#1"] == nil || *got["acme/web#1"] != 7 || got["acme/web#2"] != nil || got["acme/web#3"] == nil || *got["acme/web#3"] != 0 {
		t.Fatalf("comment counts = %v, want #1=7 #2=nil #3=0", got)
	}
	env.body = func() string {
		return `{"items":[{"queryId":"gh","provider":"github","kind":"issue","key":"acme/web#1","title":"a","state":"open","labels":[],"labelColors":{}}],"errors":[]}`
	}
	if got := read(); got["acme/web#1"] != nil {
		t.Errorf("an older Agent's refresh turned a count into %d, want unknown", *got["acme/web#1"])
	}
}
