package jirax

// The Confluence REST credential for the `confluence` wrapper
// (workspace/confluence-auth-wrapper.sh).
//
// Confluence rides the Atlassian connection that Jira stores (secrets.Data.Jira): one 3LO
// authorization covers both products, so there is no second connection to reconnect or
// refresh. Going through freshConnection is what keeps it that way: the renewal takes the same
// "jira-refresh" lock as every other caller, and Atlassian's rotating refresh tokens are
// exchanged exactly once no matter whether jira, confluence or the Agent noticed the expiry.

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// confluenceAPIPath is where the CLI's REST calls live below the API host.
const confluenceAPIPath = "/wiki/rest/api"

// ConfluenceScopes says whether the current site's OAuth authorization holds any Confluence
// scope: "granted", "missing", or "" when unknown. An API-token connection and a site whose
// scopes were never recorded are unknown, never "missing", so a connection stored before the
// scopes were recorded is not refused on a guess.
func ConfluenceScopes(c *secrets.JiraCreds) string {
	if c == nil || c.AuthKind != "oauth" {
		return ""
	}
	for _, st := range c.Sites {
		if st.CloudID != c.CloudID || len(st.Scopes) == 0 {
			continue
		}
		for _, sc := range st.Scopes {
			if strings.Contains(sc, "confluence") {
				return "granted"
			}
		}
		return "missing"
	}
	return ""
}

// RunConfluenceCred is `workspace-agent confluence-cred`: no arguments, no stdin.
func RunConfluenceCred(_ []string) { confluenceCredGet(os.Stdout, os.Stderr) }

// confluenceCredGet writes the connection as `key=value` lines, or nothing at all (an unreadable
// store, no connection):
//
//	site=    the site's own base URL, where page links point
//	domain=  the API host: api.atlassian.com for OAuth (whose tokens the site's own host
//	         refuses), the site's host for an API token
//	apipath= the REST path below the domain; /ex/confluence/<cloudId>/wiki/rest/api for OAuth
//	auth=    basic | bearer, in confluence-cli's authType vocabulary
//	login=   the account email (API token only: an OAuth bearer carries no identity)
//	token=   last, so a reader that stops early has not taken the secret
//
// A connection that exists but cannot be used says why as one `unusable=<reason>` line on errw:
// an OAuth authorization renewal failed, or the authorization is known to hold no Confluence
// scope (a connection made before they were requested), which Confluence would answer with a bare
// 401. The reason is a fixed phrase, never an error text, which can carry a response body.
func confluenceCredGet(w, errw io.Writer) {
	c, ok := freshConnection(errw)
	if !ok {
		return
	}
	unusable := func(why string) { fmt.Fprintf(errw, "unusable=%s\n", why) }
	domain, apiPath, auth, login, token := "", confluenceAPIPath, "basic", c.Email, c.Token
	if c.AuthKind == "oauth" {
		jiraBackfillScopes(&c)
		if ConfluenceScopes(&c) == "missing" {
			unusable("this Atlassian authorization has no Confluence scopes")
			return
		}
		u, err := url.Parse(CloudAPIBase)
		if err != nil || u.Host == "" {
			unusable("the stored connection is incomplete")
			return
		}
		domain, apiPath, auth, login, token = u.Host, "/ex/confluence/"+c.CloudID+confluenceAPIPath, "bearer", "", c.AccessToken
	} else if u, err := url.Parse(c.Site); err == nil {
		domain = u.Host
	}
	if domain == "" || token == "" || strings.ContainsAny(token+login+c.Site+domain+apiPath, "\r\n") {
		unusable("the stored connection is incomplete")
		return
	}
	fmt.Fprintf(w, "site=%s\ndomain=%s\napipath=%s\nauth=%s\n", c.Site, domain, apiPath, auth)
	if login != "" {
		fmt.Fprintf(w, "login=%s\n", login)
	}
	fmt.Fprintf(w, "token=%s\n", token)
}
