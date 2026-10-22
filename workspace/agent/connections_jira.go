package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
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

var jiraHTTPClient = &http.Client{Timeout: 20 * time.Second}

// jiraCloudAPIBase is Atlassian's OAuth-side API host. A var, not a const, for the same
// reason bbTokenURL is one: the site resolution and the 3LO request path are real HTTP
// conversations, and pointing them at a stub is the only honest way to pin them (there
// is no Jira account in CI or in this workspace).
var jiraCloudAPIBase = "https://api.atlassian.com"

// jiraStatus reports whether a Jira connection is stored — never the token. Site and
// the resolved account name are echoed so the card can say WHICH Jira and WHO.
func jiraStatus(s *secrets.Data) map[string]any {
	if !jiraConnected(s.Jira) {
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
	if v := jiraSoftwareScopes(c); v != "" {
		m["softwareScopes"] = v
	}
	return m
}

// jiraBoardScope is the scope every Jira Software board call needs; its absence is what makes
// `jira board list` / `jira init` answer 401 while issue calls work.
const jiraBoardScope = "read:board-scope:jira-software"

// jiraSoftwareScopes says whether the current site's authorization holds the Jira Software
// scopes: "granted", "missing", or "" when unknown. An API-token connection and a site whose
// scopes were never recorded are unknown, never "missing": the warning must not appear for
// them.
func jiraSoftwareScopes(c *secrets.JiraCreds) string {
	if c == nil || c.AuthKind != "oauth" {
		return ""
	}
	for _, st := range c.Sites {
		if st.CloudID != c.CloudID || len(st.Scopes) == 0 {
			continue
		}
		for _, sc := range st.Scopes {
			if sc == jiraBoardScope {
				return "granted"
			}
		}
		return "missing"
	}
	return ""
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

// jiraAuthHeader is Basic on the API-token path and Bearer on the OAuth one.
func jiraAuthHeader(c *secrets.JiraCreds) string {
	if c.AuthKind == "oauth" {
		return "Bearer " + c.AccessToken
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.Email+":"+c.Token))
}

// jiraAPIBase is where REST calls go — and the two paths do NOT agree.
//
// A 3LO token is not accepted by <site>.atlassian.net at all: it addresses the site
// through api.atlassian.com/ex/jira/<cloudId>. Sending an OAuth request to the site host
// fails as an auth error, which reads as "wrong token" rather than "wrong host".
// c.Site stays the human-facing base (browse links) either way.
func jiraAPIBase(c *secrets.JiraCreds) string {
	if c.AuthKind == "oauth" {
		return jiraCloudAPIBase + "/ex/jira/" + c.CloudID
	}
	return c.Site
}

// jiraConnected reports whether either path has usable credentials.
func jiraConnected(c *secrets.JiraCreds) bool {
	if c == nil {
		return false
	}
	if c.AuthKind == "oauth" {
		return c.AccessToken != "" || c.RefreshToken != ""
	}
	return c.Token != ""
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
	sites, err := jiraAccessibleSites(c)
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

// jiraAccessibleSites asks which Jira sites the authorization covers. This is the one
// call that goes to api.atlassian.com itself rather than to a site.
func jiraAccessibleSites(c *secrets.JiraCreds) ([]secrets.JiraSite, error) {
	body, status, err := jiraRequest(c, "GET", jiraCloudAPIBase+"/oauth/token/accessible-resources", nil)
	if err != nil {
		return nil, fmt.Errorf("could not reach %s", jiraCloudAPIBase)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("atlassian answered %d for accessible-resources", status)
	}
	var rows []struct {
		ID     string   `json:"id"`
		URL    string   `json:"url"`
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	out := make([]secrets.JiraSite, 0, len(rows))
	for _, r := range rows {
		if r.ID == "" || r.URL == "" {
			continue
		}
		out = append(out, secrets.JiraSite{CloudID: r.ID, URL: strings.TrimRight(r.URL, "/"), Name: r.Name, Scopes: r.Scopes})
	}
	return out, nil
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
	body, status, err := jiraRequest(c, "GET", jiraAPIBase(c)+"/rest/api/3/myself", nil)
	if err != nil {
		return me, fmt.Errorf("could not reach %s", jiraAPIBase(c))
	}
	switch {
	case status == http.StatusOK:
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return me, fmt.Errorf("Jira rejected the credentials")
	case status == http.StatusNotFound:
		// A 404 usually means the host exists but is not a Jira, or the URL is wrong.
		return me, fmt.Errorf("no Jira REST API at %s", jiraAPIBase(c))
	default:
		return me, fmt.Errorf("Jira answered %d", status)
	}
	if json.Unmarshal(body, &me) != nil {
		return jiraMe{}, fmt.Errorf("unexpected answer from %s", jiraAPIBase(c))
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
	base := jiraAPIBase(c)
	body, err := jiraGet(c, base+"/rest/api/3/search/jql"+q)
	if isJiraNotFound(err) {
		body, err = jiraGet(c, base+"/rest/api/3/search"+q)
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

type jiraHTTPError struct {
	code int
	msg  string
}

func (e *jiraHTTPError) Error() string { return e.msg }

func isJiraNotFound(err error) bool {
	je, ok := err.(*jiraHTTPError)
	return ok && (je.code == http.StatusNotFound || je.code == http.StatusGone)
}

// jiraGet issues one authenticated GET, renewing the OAuth access token when it is
// expired or refused.
//
// Two triggers, not one. The expiry stamp catches the common case before spending a
// round trip, and the 401 catches the cases the stamp cannot know about — a token
// revoked from the Atlassian side, or a clock that disagrees. Retrying at most once
// keeps a genuinely revoked authorization from looping.
func jiraGet(c *secrets.JiraCreds, u string) ([]byte, error) {
	if err := jiraEnsureFresh(c); err != nil {
		return nil, err
	}
	jiraBackfillScopes(c)
	body, status, err := jiraRequest(c, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized && c.AuthKind == "oauth" {
		if rerr := jiraRefreshNow(c); rerr == nil {
			body, status, err = jiraRequest(c, "GET", u, nil)
			if err != nil {
				return nil, err
			}
		}
	}
	if status == http.StatusOK {
		return body, nil
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, &jiraHTTPError{status, "Jira rejected the credentials (re-connect Jira)"}
	case http.StatusBadRequest:
		// A JQL syntax error lands here, and Jira's own explanation is the most helpful
		// thing to pass through.
		return nil, &jiraHTTPError{status, "Jira could not parse the JQL: " + jiraErrText(body)}
	case http.StatusTooManyRequests:
		return nil, &jiraHTTPError{status, "Jira rate limit reached"}
	}
	return nil, &jiraHTTPError{status, fmt.Sprintf("Jira answered %d", status)}
}

// jiraRequest performs one authenticated call and returns the body and status. It does
// not interpret the status — callers differ on what a 404 means (a missing endpoint to
// fall back from, versus a missing issue). GET / PUT / DELETE are re-sent on transport
// errors, 429 and 5xx (jira_retry.go); POST is sent once.
func jiraRequest(c *secrets.JiraCreds, method, u string, payload []byte) ([]byte, int, error) {
	r := jiraDo(c, method, u, payload)
	return r.body, r.status, r.err
}

// jiraScopesRecorded reports whether the current site of an OAuth connection has its scopes.
func jiraScopesRecorded(c *secrets.JiraCreds) bool {
	for _, st := range c.Sites {
		if st.CloudID == c.CloudID {
			return len(st.Scopes) > 0
		}
	}
	return true // no such site entry: nothing to fill in
}

var (
	jiraBackfillMu   sync.Mutex
	jiraBackfillLast time.Time
)

// jiraBackfillRetry spaces the attempts of one process after a failed lookup.
const jiraBackfillRetry = 10 * time.Minute

// jiraBackfillBudget bounds the lookup, so a slow Atlassian costs a jira run a delay, not a hang.
var jiraBackfillBudget = 8 * time.Second

// jiraBackfillScopes fills in the scopes of a connection stored before they were recorded, so a
// member who connected without the Jira Software scopes learns why without reconnecting first.
// c must hold a fresh access token. It only reads accessible-resources and writes scopes onto
// site entries that have none, inside the store update, so a reconnect or site switch racing
// it is never overwritten. Any failure leaves the connection unknown (as before).
func jiraBackfillScopes(c *secrets.JiraCreds) {
	if c == nil || c.AuthKind != "oauth" || c.AccessToken == "" || jiraScopesRecorded(c) {
		return
	}
	jiraBackfillMu.Lock()
	if !jiraBackfillLast.IsZero() && time.Since(jiraBackfillLast) < jiraBackfillRetry {
		jiraBackfillMu.Unlock()
		return
	}
	jiraBackfillLast = time.Now()
	jiraBackfillMu.Unlock()

	type res struct {
		sites []secrets.JiraSite
		err   error
	}
	ch := make(chan res, 1)
	cc := *c
	go func() {
		sites, err := jiraAccessibleSites(&cc)
		ch <- res{sites, err}
	}()
	var r res
	select {
	case r = <-ch:
	case <-time.After(jiraBackfillBudget):
		return
	}
	if r.err != nil {
		return
	}
	scopes := map[string][]string{}
	for _, st := range r.sites {
		if len(st.Scopes) > 0 {
			scopes[st.CloudID] = st.Scopes
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := secrets.UpdateContext(ctx, func(s *secrets.Data) error {
		if s.Jira == nil || s.Jira.AuthKind != "oauth" {
			return nil
		}
		for i := range s.Jira.Sites {
			if sc := scopes[s.Jira.Sites[i].CloudID]; len(s.Jira.Sites[i].Scopes) == 0 && len(sc) > 0 {
				s.Jira.Sites[i].Scopes = sc
			}
		}
		return nil
	})
	if err != nil {
		return
	}
	for i := range c.Sites {
		if sc := scopes[c.Sites[i].CloudID]; len(c.Sites[i].Scopes) == 0 {
			c.Sites[i].Scopes = sc
		}
	}
}

// jiraBackfillForStatus lets the Settings card learn the scopes of an older OAuth connection
// without any jira run or rail fetch. It talks to Atlassian only for an OAuth connection whose
// current site has no recorded scopes (and then at most once per jiraBackfillRetry), and reloads
// s so the answer includes what it stored and any token it renewed.
func jiraBackfillForStatus(s *secrets.Data) {
	c := s.Jira
	if c == nil || c.AuthKind != "oauth" || c.CloudID == "" || jiraScopesRecorded(c) || !jiraConnected(c) {
		return
	}
	cc := *c
	if jiraEnsureFresh(&cc) != nil {
		return
	}
	jiraBackfillScopes(&cc)
	if ns, err := secrets.Load(); err == nil {
		*s = *ns
	}
}

// jiraFreshWindow renews slightly before the stamp says to. A token that expires while
// the request is in flight comes back as a 401 that looks like a revoked authorization.
const jiraFreshWindow = 60 * time.Second

func jiraEnsureFresh(c *secrets.JiraCreds) error {
	if c.AuthKind != "oauth" || c.Expiry == 0 {
		return nil
	}
	if time.Now().Add(jiraFreshWindow).Unix() < c.Expiry {
		return nil
	}
	return jiraRefreshNow(c)
}

// jiraRefreshMu serializes the refresh grant within this process, in front of the
// cross-process lock jiraRefreshNow also takes.
//
// Not an optimisation — a correctness requirement of ROTATING refresh tokens, which
// Atlassian now mandates for new integrations. Rotation means each refresh token is
// single-use AND its reuse is treated as theft: presenting the same one twice can revoke
// the whole chain, and the member sees Jira spontaneously disconnect. Two callers can
// reach here at the same moment (a rail fetch and a post both notice the hour-old token),
// so without this lock the normal case is two identical grants racing.
var jiraRefreshMu sync.Mutex

// jiraRefreshBudget bounds one renewal: the wait for the cross-process lock and the grant.
// The jira wrapper blocks on `workspace-agent jira-cred`, so a holder that is stuck must
// cost it a delay, not a hang.
var jiraRefreshBudget = 30 * time.Second

// jiraRefreshAttemptTimeout caps one bridge call; the attempt is also cut by what is left
// of jiraRefreshBudget, so a retry never runs past the budget.
var jiraRefreshAttemptTimeout = 20 * time.Second

// jiraRefreshRetryPause is the wait before retrying a transient bridge failure.
var jiraRefreshRetryPause = time.Second

// jiraRefreshMaxAttempts bounds the bridge retries inside one renewal.
const jiraRefreshMaxAttempts = 3

// jiraBridgeRefresh runs the grant through the CP bridge, retrying a transient failure
// (transport error, timeout, CP 502/503/504) with the SAME refresh token.
//
// Why: the CP may have completed the exchange at Atlassian before the answer was lost.
// Atlassian rotates the token but accepts the old one again for a reuse interval (10 min
// by default), so presenting it again returns a fresh pair; giving up here keeps the
// retired token and the next renewal after that window gets invalid_grant. A 4xx such as
// invalid_grant is final and returns at once so the member is told to reconnect.
func jiraBridgeRefresh(ctx context.Context, b secrets.CPBridge, refreshToken string) (tok gitx.CPRefreshedToken, err error) {
	for attempt := 1; ; attempt++ {
		actx, cancel := context.WithTimeout(ctx, jiraRefreshAttemptTimeout)
		tok, err = gitx.RefreshOAuthViaCPContext(actx, b, "jira", refreshToken)
		cancel()
		if err == nil || !gitx.CPRefreshTransient(err) || attempt >= jiraRefreshMaxAttempts {
			return tok, err
		}
		select {
		case <-ctx.Done():
			return tok, err
		case <-time.After(jiraRefreshRetryPause):
		}
	}
}

// jiraRefreshNow runs the refresh grant through the CP bridge and PERSISTS the result.
//
// The Agent and every `workspace-agent jira-cred` the jira wrapper starts are separate
// processes holding the same refresh token, so the in-process mutex alone cannot keep a
// rotated token from being presented twice; the "jira-refresh" store lock is what does.
// Inside it the store is read again and is the only source: a caller whose token is no
// longer the stored one adopts what another process renewed instead of exchanging, and a
// caller whose token IS the stored one exchanges the stored refresh token, never its own
// possibly stale copy.
//
// Atlassian rotates the refresh token: the response carries a new one and retires the
// old. Not saving it strands the connection at the next expiry with nothing to renew, so
// the store write is part of the refresh, not an afterthought.
func jiraRefreshNow(c *secrets.JiraCreds) error {
	jiraRefreshMu.Lock()
	defer jiraRefreshMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), jiraRefreshBudget)
	defer cancel()
	seen := c.AccessToken
	return secrets.WithLockContext(ctx, "jira-refresh", func() error {
		cur, err := secrets.LoadContext(ctx)
		if err != nil {
			return err
		}
		d := cur.Jira
		if d == nil || d.AuthKind != "oauth" {
			return fmt.Errorf("jira connection disappeared")
		}
		if d.AccessToken != "" && d.AccessToken != seen && d.Expiry > time.Now().Add(jiraFreshWindow).Unix() {
			c.AccessToken, c.RefreshToken, c.Expiry = d.AccessToken, d.RefreshToken, d.Expiry
			return nil
		}
		b := cur.GitOAuthBridge
		if b == nil || b.BaseURL == "" || b.Token == "" {
			return fmt.Errorf("no CP bridge to refresh the Jira token")
		}
		if d.RefreshToken == "" {
			return fmt.Errorf("no refresh token stored (re-connect Jira)")
		}
		tok, err := jiraBridgeRefresh(ctx, *b, d.RefreshToken)
		if err != nil {
			return err
		}
		next := tok.RefreshToken
		if next == "" {
			next = d.RefreshToken
		}
		exp := tok.ExpiresIn
		if exp <= 0 {
			exp = 3600
		}
		expiry := time.Now().Unix() + exp
		// The grant has already spent the old refresh token, so the write gets its own
		// deadline rather than whatever is left of the budget.
		wctx, wcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer wcancel()
		err = secrets.UpdateContext(wctx, func(s *secrets.Data) error {
			if s.Jira == nil || s.Jira.AuthKind != "oauth" {
				return fmt.Errorf("jira connection disappeared")
			}
			if s.Jira.RefreshToken != d.RefreshToken {
				// Replaced since the read under the lock (a reconnect does not take it):
				// what is stored is newer than this grant's pair.
				tok.AccessToken, next, expiry = s.Jira.AccessToken, s.Jira.RefreshToken, s.Jira.Expiry
				return nil
			}
			s.Jira.AccessToken, s.Jira.RefreshToken, s.Jira.Expiry = tok.AccessToken, next, expiry
			return nil
		})
		if err != nil {
			return err
		}
		c.AccessToken, c.RefreshToken, c.Expiry = tok.AccessToken, next, expiry
		return nil
	})
}

// jiraErrText pulls the first errorMessages entry out of a Jira error body ("" when the
// body is not the shape we expect — the caller already has the status code).
func jiraErrText(body []byte) string {
	var e struct {
		ErrorMessages []string `json:"errorMessages"`
	}
	if json.Unmarshal(body, &e) == nil && len(e.ErrorMessages) > 0 {
		return e.ErrorMessages[0]
	}
	return strings.TrimSpace(string(body))
}

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
