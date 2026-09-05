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
	"encoding/json"
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

	env := &workItemEnv{st: st, mid: m.ID, hits: new(int)}
	env.body = func() string { return `{"items":[],"errors":[]}` }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/work-items/fetch" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		*env.hits++
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
		          "labels":["bug"],"repo":"acme/web","updatedAt":"2026-08-26T00:00:00Z"}],
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
}
