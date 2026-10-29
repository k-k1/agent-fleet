package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/jirax"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// Jira connection (docs/log/80 P1) — the second source of the work item inbox.
//
// Atlassian Cloud's REST v3 authenticates with HTTP Basic over
// "<account email>:<API token>", so BOTH fields are credentials and both stay in the
// container's encrypted store, exactly like the git tokens. The CP never sees them: it
// hands queries down and gets non-secret rows back (ADR 0061).
//
// Why the connection serves both the rail's list and the `jira` CLI's login, yet the list
// is fetched over REST here rather than by shelling out to `jira`: this process is the
// credential helper behind the CLI, so calling it would be a round trip through ourselves,
// and the unattended refresh must not depend on the CLI's output format (see workitems.go).
// The two are complements — this connection feeds the list, the CLI reads the body in-session.

// jiraStatus reports whether a Jira connection is stored — never the token. Site and
// the resolved account name are echoed so the card can say WHICH Jira and WHO.
func jiraStatus(s *secrets.Data) map[string]any {
	if !jirax.Connected(s.Jira) {
		return map[string]any{"connected": false}
	}
	c := s.Jira
	kind := c.AuthKind
	if kind == "" {
		kind = "token"
	}
	m := map[string]any{"connected": true, "site": c.Site, "authKind": kind}
	if kind == "token" {
		m["email"] = c.Email
	}
	if c.Account != "" {
		m["account"] = c.Account
	}
	// Report the sites even when there is only one: "you may choose" and "there is one
	// choice" are different, and without the former the user never learns they can switch.
	if len(c.Sites) > 0 {
		sites := make([]map[string]any, 0, len(c.Sites))
		for _, st := range c.Sites {
			sites = append(sites, map[string]any{"cloudId": st.CloudID, "url": st.URL, "name": st.Name})
		}
		m["sites"] = sites
		m["cloudId"] = c.CloudID
	}
	if v := jirax.SoftwareScopes(c); v != "" {
		m["softwareScopes"] = v
	}
	return m
}

type jiraConnReq struct {
	Site  string `json:"site"`
	Email string `json:"email"`
	Token string `json:"token"`
}

// normalizeJiraSite trims a pasted site URL to "https://host" — people paste the URL
// they were looking at (".../jira/software/projects/PROJ/boards/1"), and every request
// we build appends its own path.
func normalizeJiraSite(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", fmt.Errorf("site is required")
	}
	if !strings.Contains(v, "://") {
		v = "https://" + v
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("site must be a URL like https://example.atlassian.net")
	}
	if u.Scheme != "https" {
		// Over http the Basic credentials (email + token) travel in the clear, and the
		// peer is Atlassian Cloud, so there is no reason to accept anything but https.
		return "", fmt.Errorf("site must use https")
	}
	return u.Scheme + "://" + u.Host, nil
}

// handlePutJiraConn stores the connection — but only after Jira accepts it.
//
// The credentials are verified against GET /rest/api/3/myself before saving. Three
// fields (site / email / token) are three chances to typo, and without this check the
// first sign of a bad paste is an error on a rail row minutes later, which reads as
// "the feature is broken" rather than "the token is wrong".
func handlePutJiraConn(w http.ResponseWriter, r *http.Request) {
	var req jiraConnReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	site, err := normalizeJiraSite(req.Site)
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnJiraFields, err.Error())
		return
	}
	email := strings.TrimSpace(req.Email)
	token := strings.TrimSpace(req.Token)
	if email == "" || token == "" {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnJiraFields, "enter the account email and an API token")
		return
	}
	creds := &secrets.JiraCreds{Site: site, Email: email, Token: token}
	account, err := jiraAccount(creds)
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnJiraRejected, err.Error())
		return
	}
	creds.Account = account
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	s.Jira = creds
	if err := s.Save(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, jiraStatus(s))
}

// handleJiraOAuthStore — PUT /connections/jira/oauth, called by the CP at the end of the
// code grant (oauth_jira.go). The CP forwards the tokens and forgets them; everything
// that turns them into a usable connection happens here, where the token is allowed to
// live.
//
// Resolving the sites is part of storing. A 3LO token addresses a site by cloud id, so
// a connection without one cannot make a single API call — saving the tokens and
// resolving later would leave a card that says "connected" next to a rail that 401s.
func handleJiraOAuthStore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.AccessToken) == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "access_token is required")
		return
	}
	exp := req.ExpiresIn
	if exp <= 0 {
		exp = 3600
	}
	c := &secrets.JiraCreds{
		AuthKind: "oauth", AccessToken: req.AccessToken, RefreshToken: req.RefreshToken,
		Expiry: time.Now().Unix() + exp,
	}
	sites, err := jirax.AccessibleSites(c)
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, errCodeConnJiraRejected, err.Error())
		return
	}
	if len(sites) == 0 {
		// The authorization succeeded yet covers no site: either the app is missing
		// scopes or the account belongs to no Jira site. Neither is fixed by connecting
		// again, so say that instead of storing a connection.
		httpx.WriteErr(w, http.StatusBadGateway, errCodeConnJiraRejected,
			"the authorization covers no Jira site (check the app's scopes and the account's site access)")
		return
	}
	c.Sites = sites
	c.CloudID = sites[0].CloudID
	c.Site = sites[0].URL
	if me, err := jiraMyself(c); err == nil {
		c.Account, c.Login = me.DisplayName, me.EmailAddress
	}
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	s.Jira = c
	if err := s.Save(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, jiraStatus(s))
}

// handlePutJiraSite — PUT /connections/jira/site {cloudId}. One authorization can cover
// several Jira sites and only the member knows which one holds their work, so the choice
// is theirs rather than "whichever came first".
func handlePutJiraSite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CloudID string `json:"cloudId"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	if s.Jira == nil || s.Jira.AuthKind != "oauth" {
		httpx.WriteErr(w, http.StatusBadRequest, "not_connected", "Jira is not connected with OAuth")
		return
	}
	want := strings.TrimSpace(req.CloudID)
	for _, st := range s.Jira.Sites {
		if st.CloudID == want {
			s.Jira.CloudID = st.CloudID
			s.Jira.Site = st.URL
			// The display name can differ per site (another tenant's Jira is another persona).
			if me, err := jiraMyself(s.Jira); err == nil {
				s.Jira.Account, s.Jira.Login = me.DisplayName, me.EmailAddress
			}
			if err := s.Save(); err != nil {
				httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
				return
			}
			httpx.WriteJSON(w, http.StatusOK, jiraStatus(s))
			return
		}
	}
	httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "that site is not in this authorization")
}

func handleDeleteJiraConn(w http.ResponseWriter, r *http.Request) {
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	s.Jira = nil
	if err := s.Save(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"disconnected": "jira"})
}

// jiraAccount returns the display name behind the credentials (GET /rest/api/3/myself),
// and is what makes "connect" mean "these credentials work".
func jiraAccount(c *secrets.JiraCreds) (string, error) {
	me, err := jiraMyself(c)
	return me.DisplayName, err
}

type jiraMe struct {
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
}

// jiraMyself is GET /rest/api/3/myself, with its errors phrased for the connection card.
func jiraMyself(c *secrets.JiraCreds) (jiraMe, error) {
	var me jiraMe
	body, status, err := jirax.Request(c, "GET", jirax.APIBase(c)+"/rest/api/3/myself", nil)
	if err != nil {
		return me, fmt.Errorf("could not reach %s", jirax.APIBase(c))
	}
	switch {
	case status == http.StatusOK:
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return me, fmt.Errorf("Jira rejected the credentials")
	case status == http.StatusNotFound:
		// A 404 usually means the host exists but is not a Jira, or the URL is wrong.
		return me, fmt.Errorf("no Jira REST API at %s", jirax.APIBase(c))
	default:
		return me, fmt.Errorf("Jira answered %d", status)
	}
	if json.Unmarshal(body, &me) != nil {
		return jiraMe{}, fmt.Errorf("unexpected answer from %s", jirax.APIBase(c))
	}
	return me, nil
}

// jiraSearchWorkItems resolves one saved JQL query into rail rows.
//
// Two endpoints, on purpose. Atlassian replaced the classic
// GET /rest/api/3/search with /rest/api/3/search/jql (token paging instead of
// startAt/total), and which one a given site answers depends on when it was migrated.
// We try the newer path and fall back on 404/410 — the issue shape is identical, so the
// mapping below is shared. Guessing wrong in either direction would make the whole
// provider look broken.
func jiraSearchWorkItems(c *secrets.JiraCreds, queryID, jql string) ([]workItemOut, error) {
	fields := "summary,status,assignee,labels,updated,issuetype"
	q := "?jql=" + url.QueryEscape(jiraOrderedJQL(jql)) + "&maxResults=" + fmt.Sprint(workItemFetchPerQuery) + "&fields=" + url.QueryEscape(fields)
	base := jirax.APIBase(c)
	body, err := jirax.Get(c, base+"/rest/api/3/search/jql"+q)
	if jirax.IsNotFound(err) {
		body, err = jirax.Get(c, base+"/rest/api/3/search"+q)
	}
	if err != nil {
		return nil, err
	}
	return parseJiraSearchIssues(body, c.Site, queryID)
}

// jiraOrderedJQL appends `ORDER BY updated DESC` to a query that does not order itself
// (docs/log/80 §80.18.6).
//
// Because the fetch is capped at workItemFetchPerQuery, an unordered JQL means WHICH
// 50 rows survive is up to Jira. The rail says "the newest N" — the GitHub adapter passes
// sort=updated&order=desc for exactly this reason — so an arbitrary cut would make the
// rail lie. A user-written ORDER BY is left alone: they said what they wanted.
func jiraOrderedJQL(jql string) string {
	q := strings.TrimSpace(jql)
	if q == "" {
		return q
	}
	// "ORDER BY" may only appear at the end of a JQL query, so absorbing case and
	// whitespace variation is enough to find it.
	if jiraOrderBy.MatchString(q) {
		return q
	}
	return q + " ORDER BY updated DESC"
}

var jiraOrderBy = regexp.MustCompile(`(?i)\border\s+by\b`)

// parseJiraSearchIssues maps a Jira search body onto the shared row shape. Split from
// the HTTP call so the mapping is testable without a Jira (there is none in CI, and no
// account in this workspace).
func parseJiraSearchIssues(body []byte, site, queryID string) ([]workItemOut, error) {
	var jr struct {
		Issues []struct {
			Key    string `json:"key"`
			Fields struct {
				Summary string `json:"summary"`
				Updated string `json:"updated"`
				Status  struct {
					Name           string `json:"name"`
					StatusCategory struct {
						Key string `json:"key"`
					} `json:"statusCategory"`
				} `json:"status"`
				Assignee *struct {
					DisplayName string `json:"displayName"`
				} `json:"assignee"`
				Labels    []string `json:"labels"`
				IssueType *struct {
					Name string `json:"name"`
				} `json:"issuetype"`
			} `json:"fields"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(body, &jr); err != nil {
		return nil, err
	}
	out := make([]workItemOut, 0, len(jr.Issues))
	for _, is := range jr.Issues {
		assignee := ""
		if is.Fields.Assignee != nil {
			assignee = is.Fields.Assignee.DisplayName
		}
		// An issue without fields.labels leaves this nil, and a nil slice marshals to JSON
		// null, which the receiver cannot treat as an array (this exact shape once crashed
		// the Console).
		labels := is.Fields.Labels
		if labels == nil {
			labels = []string{}
		}
		typ := ""
		if is.Fields.IssueType != nil {
			typ = is.Fields.IssueType.Name
		}
		out = append(out, workItemOut{
			QueryID: queryID, Provider: "jira", Kind: "issue", Key: is.Key,
			Title:    is.Fields.Summary,
			State:    normalizeJiraState(is.Fields.Status.StatusCategory.Key),
			URL:      site + "/browse/" + is.Key,
			Assignee: assignee, Labels: labels, Type: typ, LabelColors: map[string]string{},
			// Jira has no repository. The launch target comes from the query's repoHint,
			// which is the project-to-working-copy mapping.
			Repo: "", UpdatedAt: jiraTimeToRFC3339(is.Fields.Updated),
		})
	}
	return out, nil
}

// normalizeJiraState maps Jira onto the shared vocabulary via the STATUS CATEGORY, not
// the status name. Every Jira project renames its statuses ("In review", "Awaiting QA"),
// but the category behind them is one of three fixed values — matching on names would
// break on the first custom workflow.
func normalizeJiraState(category string) string {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "new", "undefined":
		return "open"
	case "indeterminate":
		return "in_progress"
	case "done":
		return "done"
	default:
		return "other"
	}
}

// jiraTimeToRFC3339 converts Jira's "2026-08-26T10:11:12.000+0900" to RFC3339. The rail
// sorts on this string, so a value that does not parse is passed through rather than
// dropped (a row with an odd stamp still beats no row).
func jiraTimeToRFC3339(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	for _, layout := range []string{"2006-01-02T15:04:05.999-0700", time.RFC3339, time.RFC3339Nano} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return v
}
