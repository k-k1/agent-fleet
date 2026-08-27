package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// Jira connection (docs/80 P1) — the second source of the work item inbox.
//
// Atlassian Cloud's REST v3 authenticates with HTTP Basic over
// "<account email>:<API token>", so BOTH fields are credentials and both stay in the
// container's encrypted store, exactly like the git tokens. The CP never sees them: it
// hands queries down and gets non-secret rows back (ADR 0061).
//
// ⚠️ Why a connection is still required even though a Jira MCP server exists: the MCP
// only runs inside a conversation, so it cannot produce the rail's list (and its
// official remote flavour is OAuth, which docs/48 §0 puts out of af's scope). The two
// are complements — this connection feeds the list, the MCP reads the body in-session.

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
	// サイトが 1 つでも返す —— 「選べる状態にある」ことと「選択肢が 1 つ」は別で、
	// 前者を出さないと利用者は切り替えられることに気づけない。
	if len(c.Sites) > 0 {
		sites := make([]map[string]any, 0, len(c.Sites))
		for _, st := range c.Sites {
			sites = append(sites, map[string]any{"cloudId": st.CloudID, "url": st.URL, "name": st.Name})
		}
		m["sites"] = sites
		m["cloudId"] = c.CloudID
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
		// http だと Basic 認証（メール＋トークン）が平文で飛ぶ。相手は Atlassian Cloud
		// なので https 以外を受ける理由が無い。
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
// ⚠️ A 3LO token is not accepted by <site>.atlassian.net at all: it addresses the site
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
// ★ The credentials are verified against GET /rest/api/3/myself before saving. Three
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
// ★ Resolving the sites is part of storing. A 3LO token addresses a site by cloud id, so
// a connection without one cannot make a single API call — saving the tokens and
// resolving later would leave a card that says 接続済み next to a rail that 401s.
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
		// 認可は通ったのにサイトが 0。アプリのスコープ不足か、そのアカウントがどの
		// Jira サイトにも属していない —— どちらも「もう一度接続」では直らないので、
		// 接続済みにしないでそう言う。
		httpx.WriteErr(w, http.StatusBadGateway, errCodeConnJiraRejected,
			"the authorization covers no Jira site (check the app's scopes and the account's site access)")
		return
	}
	c.Sites = sites
	c.CloudID = sites[0].CloudID
	c.Site = sites[0].URL
	if name, err := jiraAccount(c); err == nil {
		c.Account = name
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
			// 表示名はサイト毎に違いうる（別テナントの Jira なら別人格）。
			if name, err := jiraAccount(s.Jira); err == nil {
				s.Jira.Account = name
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
		out = append(out, secrets.JiraSite{CloudID: r.ID, URL: strings.TrimRight(r.URL, "/"), Name: r.Name})
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
	body, status, err := jiraRequest(c, "GET", jiraAPIBase(c)+"/rest/api/3/myself", nil)
	if err != nil {
		return "", fmt.Errorf("could not reach %s", jiraAPIBase(c))
	}
	switch {
	case status == http.StatusOK:
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "", fmt.Errorf("Jira rejected the credentials")
	case status == http.StatusNotFound:
		// 404 はたいてい「サイトは実在するが Jira ではない / URL が違う」。
		return "", fmt.Errorf("no Jira REST API at %s", jiraAPIBase(c))
	default:
		return "", fmt.Errorf("Jira answered %d", status)
	}
	var me struct {
		DisplayName string `json:"displayName"`
	}
	if json.Unmarshal(body, &me) != nil {
		return "", fmt.Errorf("unexpected answer from %s", jiraAPIBase(c))
	}
	return me.DisplayName, nil
}

// jiraSearchWorkItems resolves one saved JQL query into rail rows.
//
// ⚠️ Two endpoints, on purpose. Atlassian replaced the classic
// GET /rest/api/3/search with /rest/api/3/search/jql (token paging instead of
// startAt/total), and which one a given site answers depends on when it was migrated.
// We try the newer path and fall back on 404/410 — the issue shape is identical, so the
// mapping below is shared. Guessing wrong in either direction would make the whole
// provider look broken.
func jiraSearchWorkItems(c *secrets.JiraCreds, queryID, jql string) ([]workItemOut, error) {
	fields := "summary,status,assignee,labels,updated"
	q := "?jql=" + url.QueryEscape(jql) + "&maxResults=" + fmt.Sprint(workItemFetchPerQuery) + "&fields=" + url.QueryEscape(fields)
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
// ★ Two triggers, not one. The expiry stamp catches the common case before spending a
// round trip, and the 401 catches the cases the stamp cannot know about — a token
// revoked from the Atlassian side, or a clock that disagrees. Retrying at most once
// keeps a genuinely revoked authorization from looping.
func jiraGet(c *secrets.JiraCreds, u string) ([]byte, error) {
	if err := jiraEnsureFresh(c); err != nil {
		return nil, err
	}
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
		// JQL の文法エラーはここに来る。Jira の説明文をそのまま出すのが一番親切。
		return nil, &jiraHTTPError{status, "Jira could not parse the JQL: " + jiraErrText(body)}
	case http.StatusTooManyRequests:
		return nil, &jiraHTTPError{status, "Jira rate limit reached"}
	}
	return nil, &jiraHTTPError{status, fmt.Sprintf("Jira answered %d", status)}
}

// jiraRequest performs one authenticated call and returns the body and status. It does
// not interpret the status — callers differ on what a 404 means (a missing endpoint to
// fall back from, versus a missing issue).
func jiraRequest(c *secrets.JiraCreds, method, u string, payload []byte) ([]byte, int, error) {
	var rdr io.Reader
	if payload != nil {
		rdr = strings.NewReader(string(payload))
	}
	req, err := http.NewRequest(method, u, rdr)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", jiraAuthHeader(c))
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := jiraHTTPClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("could not reach Jira")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return body, resp.StatusCode, nil
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

// jiraRefreshMu serializes the refresh grant within this process.
//
// ★ Not an optimisation — a correctness requirement of ROTATING refresh tokens, which
// Atlassian now mandates for new integrations. Rotation means each refresh token is
// single-use AND its reuse is treated as theft: presenting the same one twice can revoke
// the whole chain, and the member sees Jira spontaneously disconnect. Two callers can
// reach here at the same moment (a rail fetch and a 投稿 both notice the hour-old token),
// so without this lock the normal case is two identical grants racing.
var jiraRefreshMu sync.Mutex

// jiraRefreshNow runs the refresh grant through the CP bridge and PERSISTS the result.
//
// ⚠️ Atlassian rotates the refresh token: the response carries a new one and retires the
// old. Not saving it strands the connection at the next expiry with nothing to renew, so
// the store write is part of the refresh, not an afterthought.
func jiraRefreshNow(c *secrets.JiraCreds) error {
	jiraRefreshMu.Lock()
	defer jiraRefreshMu.Unlock()
	// 待っている間に別の呼び出しが更新し終えていることがある。その場合は**もう一度
	// 交換しない** —— 使い終わった更新トークンをもう一度出すのが、まさに rotation が
	// 「盗用」とみなす操作だからである。ディスクの新しい値を取り込んで戻る。
	if fresh, err := secrets.Load(); err == nil && fresh.Jira != nil && fresh.Jira.AuthKind == "oauth" {
		if fresh.Jira.Expiry > time.Now().Add(jiraFreshWindow).Unix() && fresh.Jira.AccessToken != "" {
			c.AccessToken = fresh.Jira.AccessToken
			c.RefreshToken = fresh.Jira.RefreshToken
			c.Expiry = fresh.Jira.Expiry
			return nil
		}
	}
	b := loadGitOAuthBridge()
	if b == nil {
		return fmt.Errorf("no CP bridge to refresh the Jira token")
	}
	if c.RefreshToken == "" {
		return fmt.Errorf("no refresh token stored (re-connect Jira)")
	}
	tok, err := refreshOAuthViaCP(*b, "jira", c.RefreshToken)
	if err != nil {
		return err
	}
	c.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		c.RefreshToken = tok.RefreshToken
	}
	exp := tok.ExpiresIn
	if exp <= 0 {
		exp = 3600
	}
	c.Expiry = time.Now().Unix() + exp
	s, err := secrets.Load()
	if err != nil {
		return err
	}
	if s.Jira == nil {
		return fmt.Errorf("jira connection disappeared")
	}
	s.Jira.AccessToken = c.AccessToken
	s.Jira.RefreshToken = c.RefreshToken
	s.Jira.Expiry = c.Expiry
	return s.Save()
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
				Labels []string `json:"labels"`
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
		// ⚠️ fields.labels が無い課題では nil のまま。nil スライスは JSON の null に
		// なり、受け手が配列として扱えない（同じ形で Console を落とした）。
		labels := is.Fields.Labels
		if labels == nil {
			labels = []string{}
		}
		out = append(out, workItemOut{
			QueryID: queryID, Provider: "jira", Kind: "issue", Key: is.Key,
			Title:    is.Fields.Summary,
			State:    normalizeJiraState(is.Fields.Status.StatusCategory.Key),
			URL:      site + "/browse/" + is.Key,
			Assignee: assignee, Labels: labels,
			// Jira はリポジトリを持たない。起動先はクエリの repoHint が決める
			// （プロジェクト → 作業コピーの対応表がそれ）。
			Repo: "", UpdatedAt: jiraTimeToRFC3339(is.Fields.Updated),
		})
	}
	return out, nil
}

// normalizeJiraState maps Jira onto the shared vocabulary via the STATUS CATEGORY, not
// the status name. Every Jira project renames its statuses ("レビュー中", "検証待ち"),
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
