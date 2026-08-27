package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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

// jiraStatus reports whether a Jira connection is stored — never the token. Site and
// the resolved account name are echoed so the card can say WHICH Jira and WHO.
func jiraStatus(s *secrets.Data) map[string]any {
	if s.Jira == nil || s.Jira.Token == "" {
		return map[string]any{"connected": false}
	}
	m := map[string]any{"connected": true, "site": s.Jira.Site, "email": s.Jira.Email}
	if s.Jira.Account != "" {
		m["account"] = s.Jira.Account
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

func jiraAuthHeader(c *secrets.JiraCreds) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.Email+":"+c.Token))
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
	req, err := http.NewRequest("GET", c.Site+"/rest/api/3/myself", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", jiraAuthHeader(c))
	req.Header.Set("Accept", "application/json")
	resp, err := jiraHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach %s", c.Site)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "", fmt.Errorf("Jira rejected the email / API token")
	case resp.StatusCode == http.StatusNotFound:
		// 404 はたいてい「サイトは実在するが Jira ではない / URL が違う」。
		return "", fmt.Errorf("no Jira REST API at %s", c.Site)
	default:
		return "", fmt.Errorf("Jira answered %d", resp.StatusCode)
	}
	var me struct {
		DisplayName string `json:"displayName"`
	}
	if json.Unmarshal(body, &me) != nil {
		return "", fmt.Errorf("unexpected answer from %s", c.Site)
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
	body, err := jiraGet(c, c.Site+"/rest/api/3/search/jql"+q)
	if isJiraNotFound(err) {
		body, err = jiraGet(c, c.Site+"/rest/api/3/search"+q)
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

func jiraGet(c *secrets.JiraCreds, u string) ([]byte, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", jiraAuthHeader(c))
	req.Header.Set("Accept", "application/json")
	resp, err := jiraHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach %s", c.Site)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusOK {
		return body, nil
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, &jiraHTTPError{resp.StatusCode, "Jira rejected the email / API token (re-connect Jira)"}
	case http.StatusBadRequest:
		// JQL の文法エラーはここに来る。Jira の説明文をそのまま出すのが一番親切。
		return nil, &jiraHTTPError{resp.StatusCode, "Jira could not parse the JQL: " + jiraErrText(body)}
	case http.StatusTooManyRequests:
		return nil, &jiraHTTPError{resp.StatusCode, "Jira rate limit reached"}
	}
	return nil, &jiraHTTPError{resp.StatusCode, fmt.Sprintf("Jira answered %d", resp.StatusCode)}
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
		out = append(out, workItemOut{
			QueryID: queryID, Provider: "jira", Kind: "issue", Key: is.Key,
			Title:    is.Fields.Summary,
			State:    normalizeJiraState(is.Fields.Status.StatusCategory.Key),
			URL:      site + "/browse/" + is.Key,
			Assignee: assignee, Labels: is.Fields.Labels,
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
