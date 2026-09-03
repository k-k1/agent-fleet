// workitems_test.go — 作業項目の受け皿（docs/log/80 / ADR 0061）。
//
// 固定するのは「壊れたときに一番痛い」4 つ:
//   - 停止中でもキャッシュが返り、取得は起きない（表示のために Workspace を起こさない）
//   - 1 本のクエリが失敗しても、そのクエリの行だけが古いまま残り、他は更新される
//   - Agent へ届かなかったときは fetched_at を進めない（進めると 5 分黙る）
//   - 「最終取得」は有効なクエリの中で一番古い時刻
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
	body func() string // stub Agent が返す /work-items/fetch の本文
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

// ★ 停止中は取得に行かず、キャッシュをそのまま返す。この機能で一番価値のある画面が
// 「止まっている Workspace を起こさずにチケットを見る」ところなので、ここが崩れると
// 設計の芯（ADR 0061 決定 1）が消える。
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

// 1 本が失敗しても、成功した方だけが差し替わる。失敗した側の行は残る（消すと
// 401 が 1 本あるだけで棚が空になる）。
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

// Agent へ届かなかったとき（停止直後・再起動中）は fetched_at を進めない。進めると
// 「届かなかったこと」を取得済みとして 5 分間黙らせてしまう。
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

// 「最終取得」は有効なクエリの中で**一番古い**時刻。新しい方を出すと、半分が古いままの
// 一覧を「たったいま取った」と言うことになる。
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

// 取得間隔（5 分）。SSE の tick は 4 秒なので、これが効かないと開いているタブの数だけ
// GitHub を叩き続ける。
func TestWorkItemsRefreshIntervalThrottles(t *testing.T) {
	env := newWorkItemEnv(t, "running")
	ctx := context.Background()
	env.addQuery(t, "q1", "自分の未完了", "assignee:@me", true)
	_ = env.st.MarkWorkItemQueryFetched(ctx, "q1", store.NowTS(), "")

	env.api.refreshNow(ctx, env.res, false)
	if *env.hits != 0 {
		t.Errorf("fetched %d times inside the interval", *env.hits)
	}
	// 期限切れにすると通る。
	stale := time.Now().UTC().Add(-2 * workItemsRefreshEvery).Format(time.RFC3339)
	_ = env.st.MarkWorkItemQueryFetched(ctx, "q1", stale, "")
	env.api.refreshNow(ctx, env.res, false)
	if *env.hits != 1 {
		t.Errorf("hits = %d, want 1 once the stamp went stale", *env.hits)
	}
	// force は間隔を無視する（更新ボタン）。
	env.api.refreshNow(ctx, env.res, true)
	if *env.hits != 2 {
		t.Errorf("hits = %d, want the forced refresh to ignore the interval", *env.hits)
	}
}

// 無効なクエリは取得対象から外れる（行は残るが、叩きには行かない）。
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
	// jira は P1 で受け付けるようになった。未知の provider は今も拒む —— 取得できない
	// 行として保存されるより、その場で断る方がよい。
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

// 台帳は (項目, セッション) で冪等。起動をやり直しても行が二重にならない。
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

// クエリを消してもキャッシュだけが消え、台帳（着手の事実）は残る。
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

// ★ 実バグの回帰（Console が真っ白になった）。Go の nil スライスは JSON の null になり、
// Console 側の item.labels.slice(...) が TypeError で落ちる —— しかもアプリに
// ErrorBoundary が無いので、セクションではなく **Console 全体**が消える。
// 「ラベルの無い課題が 1 件でも来たら」起きるので、ワイヤに null を出さないことを固定する。
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
