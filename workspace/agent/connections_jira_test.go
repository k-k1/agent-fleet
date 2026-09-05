package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// docs/log/80 P1 — the Jira-side mapping. There is no real Jira and no account in this
// environment, so the response shapes are pinned here to fix what ends up in a row.

func TestParseJiraSearchIssues(t *testing.T) {
	body := []byte(`{"issues":[
	  {"key":"PROJ-123","fields":{
	     "summary":"ログイン後に一覧が空になる",
	     "updated":"2026-08-26T10:11:12.000+0900",
	     "status":{"name":"進行中","statusCategory":{"key":"indeterminate"}},
	     "assignee":{"displayName":"山田 太郎"},
	     "labels":["bug","checkout"]}},
	  {"key":"PROJ-124","fields":{
	     "summary":"done one",
	     "updated":"2026-08-25T00:00:00.000+0000",
	     "status":{"name":"完了","statusCategory":{"key":"done"}},
	     "assignee":null,
	     "labels":[]}},
	  {"key":"PROJ-125","fields":{
	     "summary":"todo one",
	     "status":{"name":"未対応","statusCategory":{"key":"new"}}}}
	]}`)
	rows, err := parseJiraSearchIssues(body, "https://example.atlassian.net", "q9")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	got := rows[0]
	if got.QueryID != "q9" || got.Provider != "jira" || got.Kind != "issue" {
		t.Errorf("stamp: %+v", got)
	}
	if got.Key != "PROJ-123" || got.Title != "ログイン後に一覧が空になる" {
		t.Errorf("key/title: %q %q", got.Key, got.Title)
	}
	if got.URL != "https://example.atlassian.net/browse/PROJ-123" {
		t.Errorf("url = %q", got.URL)
	}
	if got.State != "in_progress" {
		t.Errorf("state = %q, want in_progress", got.State)
	}
	if got.Assignee != "山田 太郎" {
		t.Errorf("assignee = %q", got.Assignee)
	}
	if strings.Join(got.Labels, ",") != "bug,checkout" {
		t.Errorf("labels = %v", got.Labels)
	}
	// Jira has no repository. The launch target comes from the query's repoHint, so nothing may
	// be guessed and filled in here.
	if got.Repo != "" {
		t.Errorf("repo = %q, want empty (Jira has no repository)", got.Repo)
	}
	// Jira timestamps come in the "+0900" form. Without normalizing to RFC3339 (UTC), these rows
	// cannot be ordered together with GitHub's.
	if got.UpdatedAt != "2026-08-26T01:11:12Z" {
		t.Errorf("updatedAt = %q, want the UTC RFC3339 form", got.UpdatedAt)
	}
	if rows[1].State != "done" || rows[1].Assignee != "" {
		t.Errorf("row2 = %q / %q", rows[1].State, rows[1].Assignee)
	}
	if rows[2].State != "open" {
		t.Errorf("row3 = %q, want open", rows[2].State)
	}
	// An issue without labels must still not carry nil: a nil slice marshals as JSON null, which
	// the receiver cannot treat as an array (the shape that once blanked the Console).
	if rows[2].Labels == nil {
		t.Error("a label-less issue produced a nil slice, which marshals as null")
	}
	if enc, _ := json.Marshal(rows[2]); strings.Contains(string(enc), `"labels":null`) {
		t.Errorf("row wire carries a null array: %s", enc)
	}
	// The body (description) is neither fetched nor returned (ADR 0061 decision 2).
	enc, _ := json.Marshal(rows)
	if strings.Contains(string(enc), `"description"`) {
		t.Errorf("row JSON leaks a description: %s", enc)
	}
}

// The decision is made on the status CATEGORY, not the status NAME. Names are freely renamed per
// project ("in review", "awaiting verification", …), so deciding on the name breaks on the first
// custom workflow.
func TestNormalizeJiraState(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"new", "open"},
		{"undefined", "open"},
		{"indeterminate", "in_progress"},
		{"done", "done"},
		{"DONE", "done"},
		{"", "other"},
		{"進行中", "other"}, // a name, not a category: it must fall through to other
	} {
		if got := normalizeJiraState(tc.in); got != tc.want {
			t.Errorf("normalizeJiraState(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeJiraSite(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		bad      bool
	}{
		{in: "https://example.atlassian.net", want: "https://example.atlassian.net"},
		{in: " example.atlassian.net ", want: "https://example.atlassian.net"},
		// People commonly paste the URL of the page they were looking at, so drop the path.
		{in: "https://example.atlassian.net/jira/software/projects/PROJ/boards/1", want: "https://example.atlassian.net"},
		{in: "http://example.atlassian.net", bad: true}, // basic auth would travel in the clear
		{in: "", bad: true},
		{in: "https://", bad: true},
	} {
		got, err := normalizeJiraSite(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("normalizeJiraSite(%q) = %q, want an error", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("normalizeJiraSite(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

// The two endpoints are tried in order: Atlassian is migrating /search → /search/jql and which
// one a site answers on varies. This pins that a 404 on the newer one falls back to the older
// one, and that a genuine error such as 401 does NOT fall back.
func TestJiraSearchFallsBackToClassicEndpoint(t *testing.T) {
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		if r.URL.Path == "/rest/api/3/search/jql" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issues":[{"key":"PROJ-1","fields":{"summary":"x","status":{"statusCategory":{"key":"new"}}}}]}`))
	}))
	defer srv.Close()

	c := &secrets.JiraCreds{Site: srv.URL, Email: "a@example.com", Token: "t"}
	rows, err := jiraSearchWorkItems(c, "q1", "assignee = currentUser()")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(rows) != 1 || rows[0].Key != "PROJ-1" {
		t.Fatalf("rows = %+v", rows)
	}
	if len(hits) != 2 || hits[0] != "/rest/api/3/search/jql" || hits[1] != "/rest/api/3/search" {
		t.Errorf("endpoint order = %v", hits)
	}
}

func TestJiraSearchDoesNotFallBackOnAuthError(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := &secrets.JiraCreds{Site: srv.URL, Email: "a@example.com", Token: "t"}
	_, err := jiraSearchWorkItems(c, "q1", "x")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "rejected") {
		t.Errorf("error = %q, want it to name the credentials", err)
	}
	if hits != 1 {
		t.Errorf("hits = %d — a 401 must not be retried against the other endpoint", hits)
	}
}

// Credentials are verified against /rest/api/3/myself before being saved, and credentials that
// fail are NOT saved: once stored, the first symptom is an error on a rail row, which reads as
// "the feature is broken".
func TestJiraConnectVerifiesBeforeSaving(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/myself" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Basic ") {
			t.Errorf("missing basic auth header")
		}
		_, _ = w.Write([]byte(`{"displayName":"山田 太郎"}`))
	}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer bad.Close()

	put := func(site string) *httptest.ResponseRecorder {
		body := `{"site":"` + site + `","email":"a@example.com","token":"tok"}`
		w := httptest.NewRecorder()
		handlePutJiraConn(w, httptest.NewRequest("PUT", "/connections/jira", strings.NewReader(body)))
		return w
	}

	// normalizeJiraSite requires https, so to run against httptest (http) only the verification
	// part is exercised directly here.
	if _, err := jiraAccount(&secrets.JiraCreds{Site: bad.URL, Email: "a@example.com", Token: "t"}); err == nil {
		t.Error("bad credentials were accepted")
	}
	name, err := jiraAccount(&secrets.JiraCreds{Site: ok.URL, Email: "a@example.com", Token: "t"})
	if err != nil || name != "山田 太郎" {
		t.Errorf("jiraAccount = %q, %v", name, err)
	}
	// http:// is rejected at the door (basic auth would travel in the clear).
	if w := put(bad.URL); w.Code != http.StatusBadRequest {
		t.Errorf("http site accepted: %d", w.Code)
	}
	s, _ := secrets.Load()
	if s.Jira != nil {
		t.Error("a rejected connection was still written to the store")
	}
}

// --- OAuth (docs/log/80 §80.17) ---------------------------------------------------

// A 3LO token does not work against the site host. This pins that the API base changes with the
// auth kind: get it wrong and the symptom is a 401, which reads as "the token is wrong".
func TestJiraAPIBaseSwitchesWithAuthKind(t *testing.T) {
	tokenAuth := &secrets.JiraCreds{Site: "https://example.atlassian.net", Email: "a@example.com", Token: "t"}
	if got := jiraAPIBase(tokenAuth); got != "https://example.atlassian.net" {
		t.Errorf("token base = %q", got)
	}
	oauth := &secrets.JiraCreds{AuthKind: "oauth", Site: "https://example.atlassian.net", CloudID: "cid-1", AccessToken: "at"}
	if got := jiraAPIBase(oauth); got != "https://api.atlassian.com/ex/jira/cid-1" {
		t.Errorf("oauth base = %q", got)
	}
	// The auth header switches too.
	if h := jiraAuthHeader(oauth); h != "Bearer at" {
		t.Errorf("oauth header = %q", h)
	}
	if h := jiraAuthHeader(tokenAuth); !strings.HasPrefix(h, "Basic ") {
		t.Errorf("token header = %q", h)
	}
	// Site stays the URL a human sees: a browse link is not on api.atlassian.com.
	if oauth.Site != "https://example.atlassian.net" {
		t.Errorf("oauth site = %q", oauth.Site)
	}
}

// Deciding "connected" from the Token field alone makes an OAuth connection look disconnected.
func TestJiraConnected(t *testing.T) {
	if jiraConnected(nil) {
		t.Error("nil is connected")
	}
	if jiraConnected(&secrets.JiraCreds{Site: "https://x"}) {
		t.Error("a site alone is not a connection")
	}
	if !jiraConnected(&secrets.JiraCreds{Token: "t"}) {
		t.Error("api token path not recognised")
	}
	if !jiraConnected(&secrets.JiraCreds{AuthKind: "oauth", AccessToken: "at"}) {
		t.Error("oauth path not recognised")
	}
	// An expired access token with a refresh token still in hand is a live connection.
	if !jiraConnected(&secrets.JiraCreds{AuthKind: "oauth", RefreshToken: "rt"}) {
		t.Error("an expired-but-refreshable connection reads as disconnected")
	}
}

// Pins everything that happens after authorization. Resolving the site is PART OF SAVING: a 3LO
// connection without a cloud id cannot call a single API, so storing just the tokens and
// resolving later produces the most confusing state there is — the card says connected while the
// rail returns 401.
func TestJiraOAuthStoreResolvesSitesAndPicksOne(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var authSeen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authSeen = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/oauth/token/accessible-resources":
			_, _ = w.Write([]byte(`[{"id":"cid-1","url":"https://one.atlassian.net/","name":"One"},
			                        {"id":"cid-2","url":"https://two.atlassian.net","name":"Two"}]`))
		case "/ex/jira/cid-1/rest/api/3/myself":
			_, _ = w.Write([]byte(`{"displayName":"山田 太郎"}`))
		case "/ex/jira/cid-2/rest/api/3/myself":
			_, _ = w.Write([]byte(`{"displayName":"Taro on Two"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	old := jiraCloudAPIBase
	jiraCloudAPIBase = srv.URL
	defer func() { jiraCloudAPIBase = old }()

	w := httptest.NewRecorder()
	handleJiraOAuthStore(w, httptest.NewRequest("PUT", "/connections/jira/oauth",
		strings.NewReader(`{"access_token":"at","refresh_token":"rt","expires_in":3600}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("store = %d (%s)", w.Code, w.Body.String())
	}
	if authSeen != "Bearer at" {
		t.Errorf("site resolution used %q, want a Bearer token", authSeen)
	}
	s, _ := secrets.Load()
	if s.Jira == nil || s.Jira.AuthKind != "oauth" {
		t.Fatalf("stored = %+v", s.Jira)
	}
	if len(s.Jira.Sites) != 2 {
		t.Fatalf("sites = %+v", s.Jira.Sites)
	}
	// The first entry is the default. A trailing slash is stripped, or browse links get a "//".
	if s.Jira.CloudID != "cid-1" || s.Jira.Site != "https://one.atlassian.net" {
		t.Errorf("default site = %q / %q", s.Jira.CloudID, s.Jira.Site)
	}
	if s.Jira.Account != "山田 太郎" {
		t.Errorf("account = %q", s.Jira.Account)
	}
	if s.Jira.Expiry <= time.Now().Unix() {
		t.Errorf("expiry not stamped: %d", s.Jira.Expiry)
	}

	// Switching sites — only to one the authorization covers.
	w = httptest.NewRecorder()
	handlePutJiraSite(w, httptest.NewRequest("PUT", "/connections/jira/site", strings.NewReader(`{"cloudId":"cid-2"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("site switch = %d (%s)", w.Code, w.Body.String())
	}
	s, _ = secrets.Load()
	if s.Jira.CloudID != "cid-2" || s.Jira.Site != "https://two.atlassian.net" {
		t.Errorf("after switch = %q / %q", s.Jira.CloudID, s.Jira.Site)
	}
	if s.Jira.Account != "Taro on Two" {
		t.Errorf("account not re-resolved for the new site: %q", s.Jira.Account)
	}
	w = httptest.NewRecorder()
	handlePutJiraSite(w, httptest.NewRequest("PUT", "/connections/jira/site", strings.NewReader(`{"cloudId":"cid-999"}`)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("a site outside the authorization was accepted: %d", w.Code)
	}
}

// Authorization succeeded but no sites came back — a missing scope, or membership in none of
// them. Do not record it as connected.
func TestJiraOAuthStoreRefusesWhenNoSites(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	old := jiraCloudAPIBase
	jiraCloudAPIBase = srv.URL
	defer func() { jiraCloudAPIBase = old }()

	w := httptest.NewRecorder()
	handleJiraOAuthStore(w, httptest.NewRequest("PUT", "/connections/jira/oauth",
		strings.NewReader(`{"access_token":"at","refresh_token":"rt"}`)))
	if w.Code == http.StatusOK {
		t.Fatalf("stored a connection that cannot call anything: %s", w.Body.String())
	}
	s, _ := secrets.Load()
	if s.Jira != nil {
		t.Error("a refused authorization was written to the store")
	}
}

func TestJiraStatusHidesSecretsAndNamesTheAuthKind(t *testing.T) {
	s := &secrets.Data{Jira: &secrets.JiraCreds{
		AuthKind: "oauth", AccessToken: "super-secret", RefreshToken: "also-secret",
		Site: "https://example.atlassian.net", CloudID: "cid-1", Account: "山田 太郎",
		Sites: []secrets.JiraSite{{CloudID: "cid-1", URL: "https://example.atlassian.net", Name: "Example"}},
	}}
	st := jiraStatus(s)
	enc, _ := json.Marshal(st)
	for _, secret := range []string{"super-secret", "also-secret"} {
		if strings.Contains(string(enc), secret) {
			t.Fatalf("status leaked a token: %s", enc)
		}
	}
	if st["authKind"] != "oauth" {
		t.Errorf("authKind = %v", st["authKind"])
	}
	if st["cloudId"] != "cid-1" {
		t.Errorf("cloudId = %v", st["cloudId"])
	}
	// The OAuth path has no email. Emitting an empty string reads as "my email disappeared".
	if _, ok := st["email"]; ok {
		t.Errorf("oauth status carries an email field: %v", st)
	}
	// A legacy store (no authKind) keeps working, treated as the token path.
	old := &secrets.Data{Jira: &secrets.JiraCreds{Site: "https://x", Email: "a@example.com", Token: "t"}}
	if got := jiraStatus(old)["authKind"]; got != "token" {
		t.Errorf("legacy store authKind = %v, want token", got)
	}
}

// Refresh before the request when expiry is near, and PERSIST the result: Atlassian rotates the
// refresh token, so dropping the save wedges the connection at the next expiry.
func TestJiraEnsureFreshOnlyWhenOAuthAndDue(t *testing.T) {
	// Token auth has no expiry, so nothing happens — not even an error when there is no bridge.
	tokenAuth := &secrets.JiraCreds{Site: "https://x", Token: "t"}
	if err := jiraEnsureFresh(tokenAuth); err != nil {
		t.Errorf("token path tried to refresh: %v", err)
	}
	// An expiry far enough away is left alone.
	future := &secrets.JiraCreds{AuthKind: "oauth", AccessToken: "at", Expiry: time.Now().Add(time.Hour).Unix()}
	if err := jiraEnsureFresh(future); err != nil {
		t.Errorf("fresh token refreshed anyway: %v", err)
	}
	// Once expired it goes looking for the bridge, and errors out when there is none rather than
	// quietly calling with the stale token.
	t.Setenv("HOME", t.TempDir())
	expired := &secrets.JiraCreds{AuthKind: "oauth", AccessToken: "at", RefreshToken: "rt", Expiry: 1}
	if err := jiraEnsureFresh(expired); err == nil {
		t.Error("an expired token was used without a refresh")
	}
}

// A rotating refresh token is single-use. Presenting a spent one again is what Atlassian treats
// as theft, and it can revoke the whole authorization — which the user sees as "Jira disconnected
// itself". This pins that the exchange runs exactly once even when two places notice the expiry
// at the same moment.
func TestJiraRefreshIsSerializedAndNotRepeated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var mu sync.Mutex
	var seen []string // the refresh tokens received, in order
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		seen = append(seen, body.RefreshToken)
		n := len(seen)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond) // make the exchange take long enough to overlap
		_, _ = w.Write([]byte(fmt.Sprintf(`{"access_token":"at%d","refresh_token":"rt%d","expires_in":3600}`, n, n)))
	}))
	defer srv.Close()

	// Put the bridge in the store: this is the refresh route that goes through the CP.
	s, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	s.GitOAuthBridge = &secrets.CPBridge{BaseURL: srv.URL, Token: "afo_x"}
	s.Jira = &secrets.JiraCreds{AuthKind: "oauth", AccessToken: "old", RefreshToken: "rt0", Expiry: 1,
		CloudID: "cid", Site: "https://x.atlassian.net"}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	// The shape where a rail fetch and a comment post notice the expiry at the same time.
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cur, _ := secrets.Load()
			if cur == nil || cur.Jira == nil {
				return
			}
			c := *cur.Jira
			_ = jiraEnsureFresh(&c)
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("refresh grant ran %d times (%v) — a rotated token must not be presented twice", len(seen), seen)
	}
	if seen[0] != "rt0" {
		t.Errorf("exchanged %q, want the stored refresh token", seen[0])
	}
	after, _ := secrets.Load()
	if after.Jira.RefreshToken != "rt1" || after.Jira.AccessToken != "at1" {
		t.Errorf("rotation not persisted: %+v", after.Jira)
	}
	if after.Jira.Expiry <= time.Now().Unix() {
		t.Errorf("expiry not moved forward: %d", after.Jira.Expiry)
	}
}

// docs/log/80 §80.18.6 — a fetch is cut at 50 issues per query, so with an unordered JQL it is
// up to Jira WHICH 50 survive. The rail claims to show "the newest N", and that cannot be left
// undefined.
func TestJiraOrderedJQL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"assignee = currentUser() AND statusCategory != Done",
			"assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC"},
		// An ordering the user wrote is left alone, case and spacing variants included.
		{"project = G3M ORDER BY priority DESC", "project = G3M ORDER BY priority DESC"},
		{"project = G3M order by created", "project = G3M order by created"},
		{"project = G3M ORDER   BY created", "project = G3M ORDER   BY created"},
		// A JQL that merely contains "order" inside a word does not count as ordered.
		{`summary ~ "reorder"`, `summary ~ "reorder" ORDER BY updated DESC`},
		{"  ", ""},
	}
	for _, c := range cases {
		if got := jiraOrderedJQL(c.in); got != c.want {
			t.Errorf("jiraOrderedJQL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
