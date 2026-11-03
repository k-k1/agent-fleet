package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/jirax"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

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
			_, _ = w.Write([]byte(`[{"id":"cid-1","url":"https://one.atlassian.net/","name":"One","scopes":["read:jira-work","read:board-scope:jira-software"]},
			                        {"id":"cid-2","url":"https://two.atlassian.net","name":"Two","scopes":["read:jira-work"]}]`))
		case "/ex/jira/cid-1/rest/api/3/myself":
			_, _ = w.Write([]byte(`{"displayName":"山田 太郎","emailAddress":"taro@example.com"}`))
		case "/ex/jira/cid-2/rest/api/3/myself":
			_, _ = w.Write([]byte(`{"displayName":"Taro on Two"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	old := jirax.CloudAPIBase
	jirax.CloudAPIBase = srv.URL
	defer func() { jirax.CloudAPIBase = old }()

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
	// The jira CLI's login: a bearer token carries no email, so it is kept from /myself.
	if s.Jira.Login != "taro@example.com" {
		t.Errorf("login = %q", s.Jira.Login)
	}
	if s.Jira.Expiry <= time.Now().Unix() {
		t.Errorf("expiry not stamped: %d", s.Jira.Expiry)
	}

	// The scopes accessible-resources reported are kept per site, and the status names the
	// current site's verdict.
	if st := jiraStatus(s); st["softwareScopes"] != "granted" {
		t.Errorf("softwareScopes = %v, want granted", st["softwareScopes"])
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
	if st := jiraStatus(s); st["softwareScopes"] != "missing" {
		t.Errorf("softwareScopes on the second site = %v, want missing", st["softwareScopes"])
	}
	if s.Jira.Account != "Taro on Two" {
		t.Errorf("account not re-resolved for the new site: %q", s.Jira.Account)
	}
	// The second site hides the email: the first site's must not linger.
	if s.Jira.Login != "" {
		t.Errorf("login carried over from the other site: %q", s.Jira.Login)
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
	old := jirax.CloudAPIBase
	jirax.CloudAPIBase = srv.URL
	defer func() { jirax.CloudAPIBase = old }()

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
	// No recorded scopes (this store) and API-token connections: unknown, so no verdict at all.
	if _, ok := st["softwareScopes"]; ok {
		t.Errorf("unrecorded scopes produced a verdict: %v", st)
	}
	// A legacy store (no authKind) keeps working, treated as the token path.
	old := &secrets.Data{Jira: &secrets.JiraCreds{Site: "https://x", Email: "a@example.com", Token: "t"}}
	if got := jiraStatus(old)["authKind"]; got != "token" {
		t.Errorf("legacy store authKind = %v, want token", got)
	}
	if _, ok := jiraStatus(old)["softwareScopes"]; ok {
		t.Errorf("API-token connection carries a scope verdict")
	}
}

// Opening Settings alone (no jira run, no rail fetch) fills in an older connection's scopes.
func TestConnectionsGetBackfillsJiraScopes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Jira = &secrets.JiraCreds{AuthKind: "oauth", Site: "https://acme.atlassian.net", CloudID: "cid-1",
			AccessToken: "fake-at", Expiry: time.Now().Add(time.Hour).Unix(),
			Sites: []secrets.JiraSite{{CloudID: "cid-1", URL: "https://acme.atlassian.net"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token/accessible-resources" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`[{"id":"cid-1","url":"https://acme.atlassian.net","scopes":["read:jira-work"]}]`))
	}))
	old := jirax.CloudAPIBase
	jirax.CloudAPIBase = srv.URL
	restore := jirax.ResetBackfillForTest()
	t.Cleanup(func() { srv.Close(); jirax.CloudAPIBase = old; restore() })

	w := httptest.NewRecorder()
	handleConnectionsGet(w, httptest.NewRequest("GET", "/connections", nil))
	var out struct {
		Jira map[string]any `json:"jira"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Jira["softwareScopes"] != "missing" {
		t.Fatalf("jira status = %v", out.Jira)
	}
	if s, _ := secrets.Load(); len(s.Jira.Sites[0].Scopes) == 0 {
		t.Fatal("scopes not stored")
	}
}

// The card learns whether the confluence CLI can work: only an OAuth site with recorded scopes
// gets a verdict, so an API token or an older connection never shows a false warning.
func TestJiraStatusConfluenceScopes(t *testing.T) {
	site := func(scopes ...string) *secrets.Data {
		return &secrets.Data{Jira: &secrets.JiraCreds{AuthKind: "oauth", AccessToken: "fake-at", CloudID: "c1",
			Sites: []secrets.JiraSite{{CloudID: "c1", URL: "https://a.atlassian.net", Scopes: scopes}}}}
	}
	for _, tc := range []struct {
		name string
		s    *secrets.Data
		want any
	}{
		{"granted", site("read:jira-work", "search:confluence", "read:page:confluence", "write:page:confluence", "read:space:confluence"), "granted"},
		{"classic only: reconnect", site("read:jira-work", "search:confluence"), "outdated"},
		{"missing", site("read:jira-work", "offline_access"), "missing"},
		{"unrecorded", site(), nil},
		{"api token", &secrets.Data{Jira: &secrets.JiraCreds{Site: "https://a.atlassian.net", Email: "e@x", Token: "t"}}, nil},
	} {
		if got := jiraStatus(tc.s)["confluenceScopes"]; got != tc.want {
			t.Errorf("%s: confluenceScopes = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func sitesOf(t *testing.T, st map[string]any) []map[string]any {
	t.Helper()
	got, _ := st["sites"].([]map[string]any)
	return got
}

// Atlassian lists a site once per product under one cloud id; the status must report it once
// (no picker) while the stored rows stay per product for the scope union.
func TestJiraStatusDedupesSitesByCloudID(t *testing.T) {
	rows := []secrets.JiraSite{
		{CloudID: "cid-1", URL: "https://one.atlassian.net", Name: "One", Scopes: []string{"read:jira-work", "write:page:confluence"}},
		{CloudID: "cid-1", URL: "https://one.atlassian.net/wiki", Name: "One (Confluence)", Scopes: []string{"read:page:confluence", "read:space:confluence"}},
	}
	s := &secrets.Data{Jira: &secrets.JiraCreds{AuthKind: "oauth", AccessToken: "at", Expiry: time.Now().Unix() + 3600, CloudID: "cid-1", Site: "https://one.atlassian.net", Sites: rows}}
	got := sitesOf(t, jiraStatus(s))
	if len(got) != 1 || got[0]["cloudId"] != "cid-1" || got[0]["name"] != "One" || got[0]["url"] != "https://one.atlassian.net" {
		t.Fatalf("sites = %+v, want one entry from the first row", got)
	}
	if len(s.Jira.Sites) != 2 {
		t.Errorf("stored rows were modified: %+v", s.Jira.Sites)
	}
	if v := jiraStatus(s)["confluenceScopes"]; v != "granted" {
		t.Errorf("confluenceScopes = %v, want granted (scope union across rows)", v)
	}

	rows = append(rows, secrets.JiraSite{CloudID: "cid-2", URL: "https://two.atlassian.net", Name: "Two"},
		secrets.JiraSite{CloudID: "cid-2", URL: "https://two.atlassian.net", Name: "Two"})
	s.Jira.Sites = rows
	got = sitesOf(t, jiraStatus(s))
	if len(got) != 2 || got[0]["cloudId"] != "cid-1" || got[1]["cloudId"] != "cid-2" {
		t.Fatalf("two distinct sites must both be reported, in order: %+v", got)
	}
}

// Choosing a site works when its id is stored on several rows.
func TestJiraPutSiteWithDuplicateRows(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"displayName":"Taro"}`))
	}))
	defer srv.Close()
	old := jirax.CloudAPIBase
	jirax.CloudAPIBase = srv.URL
	defer func() { jirax.CloudAPIBase = old }()
	if err := (&secrets.Data{Jira: &secrets.JiraCreds{
		AuthKind: "oauth", AccessToken: "at", Expiry: time.Now().Unix() + 3600, CloudID: "cid-1", Site: "https://one.atlassian.net",
		Sites: []secrets.JiraSite{
			{CloudID: "cid-1", URL: "https://one.atlassian.net", Name: "One"},
			{CloudID: "cid-2", URL: "https://two.atlassian.net", Name: "Two", Scopes: []string{"read:jira-work"}},
			{CloudID: "cid-2", URL: "https://two.atlassian.net", Name: "Two", Scopes: []string{"read:page:confluence"}},
		},
	}}).Save(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handlePutJiraSite(w, httptest.NewRequest("PUT", "/connections/jira/site", strings.NewReader(`{"cloudId":"cid-2"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("site switch = %d (%s)", w.Code, w.Body.String())
	}
	s, _ := secrets.Load()
	if s.Jira.CloudID != "cid-2" || s.Jira.Site != "https://two.atlassian.net" {
		t.Errorf("after switch = %q / %q", s.Jira.CloudID, s.Jira.Site)
	}
	if got := sitesOf(t, jiraStatus(s)); len(got) != 2 {
		t.Errorf("sites = %+v, want 2", got)
	}
}

// A Confluence row listed first must not make its "/wiki" URL the site: the Jira base URL wins,
// both in the report and in what selecting the site stores.
func TestJiraSitesPreferBaseURLOverProductPath(t *testing.T) {
	rows := []secrets.JiraSite{
		{CloudID: "cid-1", URL: "https://one.atlassian.net/wiki", Name: "One", Scopes: []string{"read:page:confluence"}},
		{CloudID: "cid-1", URL: "https://one.atlassian.net", Name: "One", Scopes: []string{"read:jira-work"}},
	}
	s := &secrets.Data{Jira: &secrets.JiraCreds{AuthKind: "oauth", AccessToken: "at", Expiry: time.Now().Unix() + 3600, CloudID: "cid-1", Site: "x", Sites: rows}}
	got := sitesOf(t, jiraStatus(s))
	if len(got) != 1 || got[0]["url"] != "https://one.atlassian.net" {
		t.Fatalf("sites = %+v, want the base URL", got)
	}
}
