package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Work item inbox (docs/log/80 / ADR 0061) — external tickets (GitHub Issue / PR; Jira in
// P1) listed in the left rail so a session can be started from one.
//
// The split that makes this work: the CP owns the saved queries and a cache of
// NON-SECRET metadata, the Workspace Agent owns the provider tokens and does the
// fetching. The CP hands the queries down to the Agent and gets rows back — the token
// never leaves the container, and the CP never learns a ticket's description.
//
// Why the cache exists at all: picking a ticket happens BEFORE a session is started,
// which is when the Workspace is usually stopped. Without the cache the rail is empty
// exactly when it matters, and the alternative — waking the Workspace to render a list —
// re-opens the "Workspace never stops" hole docs/log/75 closed. So a stopped Workspace shows
// the cache with its "last fetched" stamp, and only the Start button starts anything.
const (
	// workItemsRefreshEvery rate-limits provider calls. The SSE tick is 4s; without this
	// every tick of every open tab would hit the GitHub API.
	workItemsRefreshEvery = 5 * time.Minute
	// workItemsMaxQueries caps saved queries per membership. Not a UI nicety: the rail
	// stays readable and the provider budget stays bounded (docs/log/80 §80.12).
	workItemsMaxQueries = 10
	// workItemsFetchTimeout bounds one Agent round trip (which fans out to the provider).
	workItemsFetchTimeout = 30 * time.Second
)

// workItemProviders is the set a saved query may name. The Agent holds the adapters
// (workspace/agent/workitems*.go); this is only the gate that stops a query being stored
// which nothing could ever resolve.
var workItemProviders = map[string]bool{"github": true, "jira": true, "bitbucket": true}

// workItemFetchInFlight guards against stacking refreshes: every open tab's SSE tick
// asks whether a refresh is due, and without this each of them would spawn one.
// Package level (not a field) because the REST face and the events face construct
// separate workItemsAPI values but share the same memberships.
var workItemFetchInFlight sync.Map // membershipID -> struct{}

type workItemsAPI struct {
	memberAuth
	store store.WorkItemStore
}

func newWorkItemsAPI(m *manager) workItemsAPI { return workItemsAPI{memberAuth{m}, m.store} }

// workItemDTO is the wire shape of one cached row. Note what is absent: body, comments,
// attachments. Those are read inside the session with `gh` / the Jira MCP.
type workItemDTO struct {
	ID        string   `json:"id"`
	QueryID   string   `json:"queryId"`
	Provider  string   `json:"provider"`
	Kind      string   `json:"kind"`
	Key       string   `json:"key"`
	Title     string   `json:"title"`
	State     string   `json:"state"`
	URL       string   `json:"url"`
	Assignee  string   `json:"assignee"`
	Labels    []string `json:"labels"`
	Repo      string   `json:"repo"`
	UpdatedAt string   `json:"updatedAt"`
}

type workItemQueryDTO struct {
	ID        string `json:"id"`
	Provider  string `json:"provider"`
	Label     string `json:"label"`
	Query     string `json:"query"`
	RepoHint  string `json:"repoHint"`
	Enabled   bool   `json:"enabled"`
	Position  int    `json:"position"`
	FetchedAt string `json:"fetchedAt"`
	LastError string `json:"lastError"`
}

type workItemSessionDTO struct {
	ID          string `json:"id"`
	Provider    string `json:"provider"`
	ItemKey     string `json:"itemKey"`
	SessionName string `json:"sessionName"`
	Repo        string `json:"repo"`
	Branch      string `json:"branch"`
	CreatedAt   string `json:"createdAt"`
}

func workItemToDTO(w store.WorkItem) workItemDTO {
	return workItemDTO{ID: w.ID, QueryID: w.QueryID, Provider: w.Provider, Kind: w.Kind,
		Key: w.Key, Title: w.Title, State: w.State, URL: w.URL, Assignee: w.Assignee,
		Labels: splitLabels(w.Labels), Repo: w.Repo, UpdatedAt: w.UpdatedAt}
}

func workItemQueryToDTO(q store.WorkItemQuery) workItemQueryDTO {
	return workItemQueryDTO{ID: q.ID, Provider: q.Provider, Label: q.Label, Query: q.Query,
		RepoHint: q.RepoHint, Enabled: q.Enabled, Position: q.Position,
		FetchedAt: q.FetchedAt, LastError: q.LastError}
}

func workItemSessionToDTO(s store.WorkItemSession) workItemSessionDTO {
	return workItemSessionDTO{ID: s.ID, Provider: s.Provider, ItemKey: s.ItemKey,
		SessionName: s.SessionName, Repo: s.Repo, Branch: s.Branch, CreatedAt: s.CreatedAt}
}

// splitLabels turns the stored comma-separated column into a slice.
//
// It returns an EMPTY slice, never nil. A nil slice marshals to JSON `null`, and the
// Console then does `item.labels.slice(...)` on it — which took the whole Console to a
// white screen the first time a ticket without labels reached the rail (there is no
// error boundary, so one null field kills the app, not just the section).
func splitLabels(s string) []string {
	if strings.TrimSpace(s) == "" {
		return []string{}
	}
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// workItemsPayloadWire is the response of GET /api/workitems and of the Refresh button's
// POST (the Console's `WorkItemPayload`, console/src/features/workitems/read.ts).
//
// was: map[string]any{"items":…, "queries":…, "sessions":…, "fetchedAt":…, "running":…}
//
// All five keys are unconditional, so none of them takes omitempty. The three slices are
// built with make(…, 0, n) by the caller and are therefore never nil, so they marshal to
// `[]` — a nil slice would be `null`, which is a different thing to the Console. Three
// call sites (list once, refresh twice) share this shape.
type workItemsPayloadWire struct {
	Items     []workItemDTO        `json:"items"`
	Queries   []workItemQueryDTO   `json:"queries"`
	Sessions  []workItemSessionDTO `json:"sessions"`
	FetchedAt string               `json:"fetchedAt"`
	Running   bool                 `json:"running"`
}

// workItemsPayload composes the GET /api/work-items body, shared by the REST handler and
// the /api/events push channel so both emit the identical shape.
//
// state is passed in because the events tick already resolved it — on ecs-ec2 State() is
// live AWS calls, and pulling it twice per tick doubles them (same reasoning as
// statsPayload).
func (a workItemsAPI) workItemsPayload(ctx context.Context, res *resolved, state string) (workItemsPayloadWire, *apiError) {
	mid := res.mv.MembershipID
	if state == "running" {
		// Fire-and-forget: the tick must never block on a provider round trip. The
		// result lands in the DB and the next tick emits it as a diff.
		a.refreshAsync(res, false)
	}
	queries, err := a.store.ListWorkItemQueries(ctx, mid)
	if err != nil {
		return workItemsPayloadWire{}, internalErr(err)
	}
	items, err := a.store.ListWorkItems(ctx, mid)
	if err != nil {
		return workItemsPayloadWire{}, internalErr(err)
	}
	sessions, err := a.store.ListWorkItemSessions(ctx, mid)
	if err != nil {
		return workItemsPayloadWire{}, internalErr(err)
	}
	qOut := make([]workItemQueryDTO, 0, len(queries))
	fetchedAt := ""
	for _, q := range queries {
		qOut = append(qOut, workItemQueryToDTO(q))
		// "Last fetched" is the OLDEST time among the enabled queries. Showing the
		// newest would claim that rows left stale by half the queries failing were
		// fetched just now.
		if q.Enabled && (fetchedAt == "" || (q.FetchedAt != "" && q.FetchedAt < fetchedAt)) {
			fetchedAt = q.FetchedAt
		}
	}
	iOut := make([]workItemDTO, 0, len(items))
	for _, w := range items {
		iOut = append(iOut, workItemToDTO(w))
	}
	sOut := make([]workItemSessionDTO, 0, len(sessions))
	for _, s := range sessions {
		sOut = append(sOut, workItemSessionToDTO(s))
	}
	return workItemsPayloadWire{
		Items: iOut, Queries: qOut, Sessions: sOut,
		FetchedAt: fetchedAt, Running: state == "running",
	}, nil
}

func (a workItemsAPI) list(w http.ResponseWriter, r *http.Request, res *resolved) {
	out, aerr := a.workItemsPayload(r.Context(), res, res.rt.State(r.Context()))
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// refresh is the Refresh button: forced (ignores the interval) and SYNCHRONOUS, because a
// button that returns the same stale list reads as broken. The list face stays async.
func (a workItemsAPI) refresh(w http.ResponseWriter, r *http.Request, res *resolved) {
	ctx := r.Context()
	state := res.rt.State(ctx)
	if state != "running" {
		// Never wake a Workspace just to render (ADR 0061 decision 1). When it is
		// stopped, return the cache as-is and let running=false explain why it is old.
		out, aerr := a.workItemsPayload(ctx, res, state)
		if aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	a.refreshNow(ctx, res, true)
	out, aerr := a.workItemsPayload(ctx, res, state)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// refreshAsync runs refreshNow in the background, at most one per membership.
func (a workItemsAPI) refreshAsync(res *resolved, force bool) {
	mid := res.mv.MembershipID
	if _, busy := workItemFetchInFlight.LoadOrStore(mid, struct{}{}); busy {
		return
	}
	go func() {
		defer workItemFetchInFlight.Delete(mid)
		// Not the caller's request ctx: it is cancelled as soon as the tick ends.
		ctx, cancel := context.WithTimeout(context.Background(), workItemsFetchTimeout)
		defer cancel()
		a.refreshNow(ctx, res, force)
	}()
}

// refreshNow fetches the due queries through the Agent and swaps their cached rows.
// Best effort: every failure is recorded on the query row (last_error) and shown in the
// rail rather than failing the request — a broken query must not hide the working ones.
func (a workItemsAPI) refreshNow(ctx context.Context, res *resolved, force bool) {
	mid := res.mv.MembershipID
	queries, err := a.store.ListWorkItemQueries(ctx, mid)
	if err != nil {
		return
	}
	cutoff := time.Now().UTC().Add(-workItemsRefreshEvery).Format(time.RFC3339)
	due := make([]store.WorkItemQuery, 0, len(queries))
	for _, q := range queries {
		if !q.Enabled {
			continue
		}
		if force || q.FetchedAt == "" || q.FetchedAt < cutoff {
			due = append(due, q)
		}
		if len(due) >= workItemsMaxQueries {
			break
		}
	}
	if len(due) == 0 {
		return
	}
	rows, errs, err := fetchWorkItemsFromAgent(ctx, res.rt, due)
	now := store.NowTS()
	if err != nil {
		// The Agent was unreachable (just stopped, or restarting). Do not advance
		// fetched_at: doing so would pass the failure off as a fetch and stay quiet
		// about it for the next five minutes.
		for _, q := range due {
			_ = a.store.MarkWorkItemQueryFetched(ctx, q.ID, q.FetchedAt, err.Error())
		}
		return
	}
	ok := make([]string, 0, len(due))
	items := make([]store.WorkItem, 0, 64)
	for _, q := range due {
		if msg := errs[q.ID]; msg != "" {
			// Keep a failed query's cached rows. Dropping them empties the rail on a
			// single 401.
			_ = a.store.MarkWorkItemQueryFetched(ctx, q.ID, now, msg)
			continue
		}
		ok = append(ok, q.ID)
		for _, it := range rows[q.ID] {
			it.ID = store.NewID()
			it.MembershipID = mid
			it.QueryID = q.ID
			it.FetchedAt = now
			items = append(items, it)
		}
		_ = a.store.MarkWorkItemQueryFetched(ctx, q.ID, now, "")
	}
	if len(ok) > 0 {
		_ = a.store.ReplaceWorkItems(ctx, mid, ok, items)
	}
}

// agentWorkItemsReq/Resp is the CP -> Agent contract. The CP sends the queries it owns
// and gets non-secret rows back; no token crosses in either direction.
type agentWorkItemsReq struct {
	Queries []agentWorkItemQuery `json:"queries"`
}

type agentWorkItemQuery struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Query    string `json:"query"`
}

type agentWorkItemsResp struct {
	Items []struct {
		QueryID   string   `json:"queryId"`
		Provider  string   `json:"provider"`
		Kind      string   `json:"kind"`
		Key       string   `json:"key"`
		Title     string   `json:"title"`
		State     string   `json:"state"`
		URL       string   `json:"url"`
		Assignee  string   `json:"assignee"`
		Labels    []string `json:"labels"`
		Repo      string   `json:"repo"`
		UpdatedAt string   `json:"updatedAt"`
	} `json:"items"`
	Errors []struct {
		QueryID string `json:"queryId"`
		Message string `json:"message"`
	} `json:"errors"`
}

// fetchWorkItemsFromAgent asks the running Agent to resolve the queries. Returns the rows
// per query id and the per-query error messages; a transport-level failure comes back as
// the error (the caller then keeps the previous fetched_at, see refreshNow).
func fetchWorkItemsFromAgent(ctx context.Context, rt runtime.Runtime, queries []store.WorkItemQuery) (map[string][]store.WorkItem, map[string]string, error) {
	in := agentWorkItemsReq{Queries: make([]agentWorkItemQuery, 0, len(queries))}
	for _, q := range queries {
		in.Queries = append(in.Queries, agentWorkItemQuery{ID: q.ID, Provider: q.Provider, Query: q.Query})
	}
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rt.Endpoint()+"/work-items/fetch", bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := rt.Token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// A 404 means an Agent from before the fleet was rebuilt; say "unsupported"
		// rather than letting it read as a failure.
		if resp.StatusCode == http.StatusNotFound {
			return nil, nil, &httpStatusError{resp.StatusCode, "this workspace agent does not support work items yet"}
		}
		return nil, nil, &httpStatusError{resp.StatusCode, "agent refused the work item fetch"}
	}
	var out agentWorkItemsResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, nil, err
	}
	rows := map[string][]store.WorkItem{}
	for _, it := range out.Items {
		rows[it.QueryID] = append(rows[it.QueryID], store.WorkItem{
			Provider: it.Provider, Kind: it.Kind, Key: it.Key, Title: it.Title,
			State: it.State, URL: it.URL, Assignee: it.Assignee,
			Labels: strings.Join(it.Labels, ","), Repo: it.Repo, UpdatedAt: it.UpdatedAt,
		})
	}
	errs := map[string]string{}
	for _, e := range out.Errors {
		errs[e.QueryID] = e.Message
	}
	return rows, errs, nil
}

type httpStatusError struct {
	code int
	msg  string
}

func (e *httpStatusError) Error() string { return e.msg }

// --- comment back to the ticket ----------------------------------------------

// comment relays a human-approved draft to the Agent, which holds the tokens.
//
// Requires a running Workspace and does NOT start one. Everywhere else in this file
// that rule protects the idle clock; here it also keeps the meaning of the button
// honest — posting is a write against someone else's tracker, so it happens when the
// user is present and their workspace is up, not as a side effect of pressing a button
// on a stopped one.
func (a workItemsAPI) comment(w http.ResponseWriter, r *http.Request, res *resolved) {
	var in struct {
		Provider string `json:"provider"`
		Key      string `json:"key"`
		Body     string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	if strings.TrimSpace(in.Key) == "" || strings.TrimSpace(in.Body) == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "key and body are required"})
		return
	}
	ctx := r.Context()
	if res.rt.State(ctx) != "running" {
		writeAPIErr(w, &apiError{http.StatusConflict, "workspace_stopped",
			"start the workspace to post the comment (the tracker credentials live in it)"})
		return
	}
	payload, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, res.rt.Endpoint()+"/work-items/comment", bytes.NewReader(payload))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := res.rt.Token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		writeAPIErr(w, &apiError{http.StatusBadGateway, "agent_unreachable", err.Error()})
		return
	}
	defer resp.Body.Close()
	// Pass the Agent's response through verbatim, success or failure. The provider's own
	// refusal is the most informative thing here; rephrasing it in the CP erases whether
	// the problem was permission or a missing issue.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// --- saved queries -----------------------------------------------------------

func (a workItemsAPI) listQueries(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	rows, err := a.store.ListWorkItemQueries(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]workItemQueryDTO, 0, len(rows))
	for _, q := range rows {
		out = append(out, workItemQueryToDTO(q))
	}
	writeJSON(w, http.StatusOK, out)
}

// validateWorkItemQuery normalizes an incoming query. An unknown provider is refused
// rather than saved as a row that can never fetch.
//
// This list is the ONLY place the Control Plane knows a provider exists. Everything
// else here is pass-through — the query text, the rows and the per-query error message
// all belong to the Agent, which is the only side holding a token (ADR 0061 decision 6.5).
// So adding Bitbucket cost one line here and nothing else in the CP.
func validateWorkItemQuery(mv store.MembershipView, in workItemQueryDTO) (store.WorkItemQuery, *apiError) {
	q := store.WorkItemQuery{
		MembershipID: mv.MembershipID,
		Provider:     strings.TrimSpace(in.Provider),
		Label:        strings.TrimSpace(in.Label),
		Query:        strings.TrimSpace(in.Query),
		RepoHint:     strings.TrimSpace(in.RepoHint),
		Enabled:      in.Enabled,
		Position:     in.Position,
	}
	if q.Provider == "" {
		q.Provider = "github"
	}
	if !workItemProviders[q.Provider] {
		return store.WorkItemQuery{}, &apiError{http.StatusBadRequest, "bad_provider", "provider must be github, jira or bitbucket"}
	}
	if q.Query == "" {
		return store.WorkItemQuery{}, &apiError{http.StatusBadRequest, "bad_query", "query is required"}
	}
	if len(q.Query) > 400 {
		return store.WorkItemQuery{}, &apiError{http.StatusBadRequest, "bad_query", "query is too long"}
	}
	if q.Label == "" {
		q.Label = q.Query
	}
	return q, nil
}

func (a workItemsAPI) createQuery(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	var in workItemQueryDTO
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	q, aerr := validateWorkItemQuery(mv, in)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	rows, err := a.store.ListWorkItemQueries(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if len(rows) >= workItemsMaxQueries {
		writeAPIErr(w, &apiError{http.StatusConflict, "too_many_queries", "the saved query limit is reached"})
		return
	}
	for _, row := range rows {
		if row.Position >= q.Position {
			q.Position = row.Position + 1
		}
	}
	q.ID = store.NewID()
	q.CreatedAt = store.NowTS()
	if err := a.store.CreateWorkItemQuery(r.Context(), q); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, workItemQueryToDTO(q))
}

func (a workItemsAPI) updateQuery(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	cur, ok, err := a.store.GetWorkItemQuery(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok || cur.MembershipID != mv.MembershipID {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "no such query"})
		return
	}
	in := workItemQueryToDTO(cur)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	q, aerr := validateWorkItemQuery(mv, in)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	q.ID = cur.ID
	q.CreatedAt = cur.CreatedAt
	if err := a.store.UpdateWorkItemQuery(r.Context(), q); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, workItemQueryToDTO(q))
}

func (a workItemsAPI) deleteQuery(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	if err := a.store.DeleteWorkItemQuery(r.Context(), r.PathValue("id"), mv.MembershipID); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- ledger ------------------------------------------------------------------

// createSession records "this ticket was started as that session". Idempotent per
// (item, session) so a retried launch does not double the row.
func (a workItemsAPI) createSession(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	var in workItemSessionDTO
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	rec := store.WorkItemSession{
		MembershipID: mv.MembershipID,
		Provider:     strings.TrimSpace(in.Provider),
		ItemKey:      strings.TrimSpace(in.ItemKey),
		SessionName:  strings.TrimSpace(in.SessionName),
		Repo:         strings.TrimSpace(in.Repo),
		Branch:       strings.TrimSpace(in.Branch),
	}
	if rec.ItemKey == "" || rec.SessionName == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "itemKey and sessionName are required"})
		return
	}
	if rec.Provider == "" {
		rec.Provider = "github"
	}
	rows, err := a.store.ListWorkItemSessions(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	for _, row := range rows {
		if row.ItemKey == rec.ItemKey && row.SessionName == rec.SessionName {
			writeJSON(w, http.StatusOK, workItemSessionToDTO(row))
			return
		}
	}
	rec.ID = store.NewID()
	rec.CreatedAt = store.NowTS()
	if err := a.store.CreateWorkItemSession(r.Context(), rec); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, workItemSessionToDTO(rec))
}

func (a workItemsAPI) deleteSession(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	if err := a.store.DeleteWorkItemSession(r.Context(), r.PathValue("id"), mv.MembershipID); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// sortQueriesByPosition keeps the rail order stable for callers that build their own
// list (tests, and the rail's reorder path).
func sortQueriesByPosition(rows []store.WorkItemQuery) {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Position < rows[j].Position })
}
