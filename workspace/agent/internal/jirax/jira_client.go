// Package jirax is the Agent's Jira REST client (auth header, API base, OAuth renewal and the
// retrying call) and the `workspace-agent jira-*` subcommands the jira PATH wrapper runs. The
// connection handlers and the work item adapter stay in package main and call it.
package jirax

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

var jiraHTTPClient = &http.Client{Timeout: 20 * time.Second}

// CloudAPIBase is Atlassian's OAuth-side API host. A var, not a const, for the same
// reason bbTokenURL is one: the site resolution and the 3LO request path are real HTTP
// conversations, and pointing them at a stub is the only honest way to pin them (there
// is no Jira account in CI or in this workspace).
var CloudAPIBase = "https://api.atlassian.com"

// jiraBoardScope is the scope every Jira Software board call needs; its absence is what makes
// `jira board list` / `jira init` answer 401 while issue calls work.
const jiraBoardScope = "read:board-scope:jira-software"

// SoftwareScopes says whether the current site's authorization holds the Jira Software
// scopes: "granted", "missing", or "" when unknown. An API-token connection and a site whose
// scopes were never recorded are unknown, never "missing": the warning must not appear for
// them.
func SoftwareScopes(c *secrets.JiraCreds) string {
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

// jiraAuthHeader is Basic on the API-token path and Bearer on the OAuth one.
func jiraAuthHeader(c *secrets.JiraCreds) string {
	if c.AuthKind == "oauth" {
		return "Bearer " + c.AccessToken
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.Email+":"+c.Token))
}

// APIBase is where REST calls go — and the two paths do NOT agree.
//
// A 3LO token is not accepted by <site>.atlassian.net at all: it addresses the site
// through api.atlassian.com/ex/jira/<cloudId>. Sending an OAuth request to the site host
// fails as an auth error, which reads as "wrong token" rather than "wrong host".
// c.Site stays the human-facing base (browse links) either way.
func APIBase(c *secrets.JiraCreds) string {
	if c.AuthKind == "oauth" {
		return CloudAPIBase + "/ex/jira/" + c.CloudID
	}
	return c.Site
}

// Connected reports whether either path has usable credentials.
func Connected(c *secrets.JiraCreds) bool {
	if c == nil {
		return false
	}
	if c.AuthKind == "oauth" {
		return c.AccessToken != "" || c.RefreshToken != ""
	}
	return c.Token != ""
}

// AccessibleSites asks which Jira sites the authorization covers. This is the one
// call that goes to api.atlassian.com itself rather than to a site.
func AccessibleSites(c *secrets.JiraCreds) ([]secrets.JiraSite, error) {
	body, status, err := Request(c, "GET", CloudAPIBase+"/oauth/token/accessible-resources", nil)
	if err != nil {
		return nil, fmt.Errorf("could not reach %s", CloudAPIBase)
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

type HTTPError struct {
	Code int
	msg  string
}

func (e *HTTPError) Error() string { return e.msg }

func IsNotFound(err error) bool {
	je, ok := err.(*HTTPError)
	return ok && (je.Code == http.StatusNotFound || je.Code == http.StatusGone)
}

// Get issues one authenticated GET, renewing the OAuth access token when it is
// expired or refused.
//
// Two triggers, not one. The expiry stamp catches the common case before spending a
// round trip, and the 401 catches the cases the stamp cannot know about — a token
// revoked from the Atlassian side, or a clock that disagrees. Retrying at most once
// keeps a genuinely revoked authorization from looping.
func Get(c *secrets.JiraCreds, u string) ([]byte, error) {
	if err := EnsureFresh(c); err != nil {
		return nil, err
	}
	jiraBackfillScopes(c)
	body, status, err := Request(c, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized && c.AuthKind == "oauth" {
		if rerr := RefreshNow(c); rerr == nil {
			body, status, err = Request(c, "GET", u, nil)
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
		return nil, &HTTPError{status, "Jira rejected the credentials (re-connect Jira)"}
	case http.StatusBadRequest:
		// A JQL syntax error lands here, and Jira's own explanation is the most helpful
		// thing to pass through.
		return nil, &HTTPError{status, "Jira could not parse the JQL: " + ErrText(body)}
	case http.StatusTooManyRequests:
		return nil, &HTTPError{status, "Jira rate limit reached"}
	}
	return nil, &HTTPError{status, fmt.Sprintf("Jira answered %d", status)}
}

// Request performs one authenticated call and returns the body and status. It does
// not interpret the status — callers differ on what a 404 means (a missing endpoint to
// fall back from, versus a missing issue). GET / PUT / DELETE are re-sent on transport
// errors, 429 and 5xx (jira_retry.go); POST is sent once.
func Request(c *secrets.JiraCreds, method, u string, payload []byte) ([]byte, int, error) {
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

// ResetBackfillForTest clears the once-per-jiraBackfillRetry throttle so a test outside this
// package can make the next lookup run; without it, whichever earlier test spent the attempt
// decides whether a later one sees scopes.
func ResetBackfillForTest() (restore func()) {
	jiraBackfillMu.Lock()
	old := jiraBackfillLast
	jiraBackfillLast = time.Time{}
	jiraBackfillMu.Unlock()
	return func() {
		jiraBackfillMu.Lock()
		jiraBackfillLast = old
		jiraBackfillMu.Unlock()
	}
}

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
		sites, err := AccessibleSites(&cc)
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

// BackfillForStatus lets the Settings card learn the scopes of an older OAuth connection
// without any jira run or rail fetch. It talks to Atlassian only for an OAuth connection whose
// current site has no recorded scopes (and then at most once per jiraBackfillRetry), and reloads
// s so the answer includes what it stored and any token it renewed.
func BackfillForStatus(s *secrets.Data) {
	c := s.Jira
	if c == nil || c.AuthKind != "oauth" || c.CloudID == "" || jiraScopesRecorded(c) || !Connected(c) {
		return
	}
	cc := *c
	if EnsureFresh(&cc) != nil {
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

func EnsureFresh(c *secrets.JiraCreds) error {
	if c.AuthKind != "oauth" || c.Expiry == 0 {
		return nil
	}
	if time.Now().Add(jiraFreshWindow).Unix() < c.Expiry {
		return nil
	}
	return RefreshNow(c)
}

// jiraRefreshMu serializes the refresh grant within this process, in front of the
// cross-process lock RefreshNow also takes.
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
		t := time.NewTimer(jiraRefreshRetryPause)
		select {
		case <-ctx.Done():
			t.Stop()
			return tok, fmt.Errorf("%w (last bridge error: %v)", ctx.Err(), err)
		case <-t.C:
		}
	}
}

// RefreshNow runs the refresh grant through the CP bridge and PERSISTS the result.
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
//
// Worst-case hold of the "jira-refresh" lock is the budget plus the 10 s persist, which can
// exceed a waiter's own 30 s (the jira wrapper's jira-cred bound). Accepted: the waiter fails
// once with a generic error and the next call adopts the stored pair. The persist cannot move
// out of the lock — a second process would exchange the retired token.
func RefreshNow(c *secrets.JiraCreds) error {
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

// ErrText pulls the first errorMessages entry out of a Jira error body ("" when the
// body is not the shape we expect — the caller already has the status code).
func ErrText(body []byte) string {
	var e struct {
		ErrorMessages []string `json:"errorMessages"`
	}
	if json.Unmarshal(body, &e) == nil && len(e.ErrorMessages) > 0 {
		return e.ErrorMessages[0]
	}
	return strings.TrimSpace(string(body))
}
