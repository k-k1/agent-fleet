package main

// The Jira REST credential for the `jira` wrapper (workspace/jira-auth-wrapper.sh).
//
// Jira is not a git host, so `git credential fill` has nothing to say about it; this
// subcommand is the wrapper's only source. It answers on every call, which is what lets a
// one-hour OAuth token outlive the session that uses it: the wrapper never holds a token
// longer than one jira run.

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// jiraCredStoreWait bounds the wait for the store: the wrapper blocks on this process, so a
// writer wedged on the lock must cost jira a delay, not a hang.
var jiraCredStoreWait = 30 * time.Second

// jiraCredFreshWindow is wider than jiraFreshWindow because the token is handed to a whole
// jira run rather than one request: `jira issue list` pages, and `jira init` makes a dozen
// calls. A token that dies midway fails the command with a 401 that reads like a revoked
// connection.
const jiraCredFreshWindow = 5 * time.Minute

// runJiraCred is `workspace-agent jira-cred`: no arguments, no stdin.
func runJiraCred(_ []string) { jiraCredGet(os.Stdout) }

// jiraCredGet writes the connection as `key=value` lines, or nothing at all (an unreadable
// store, no connection, an OAuth token that is expired and cannot be renewed):
//
//	site=   the site's own base URL, where browse links point
//	server= where REST calls go; the site for an API token, api.atlassian.com/ex/jira/<cloudId>
//	        for OAuth, whose tokens the site's own host refuses
//	auth=   basic | bearer, in jira-cli's auth_type vocabulary
//	login=  the account email; absent when an OAuth account hides it
//	software= "missing" when the OAuth authorization is known to lack the Jira Software scopes
//	        (boards, sprints, epics); absent when granted or unknown (API token, old store)
//	token=  last, so a reader that stops early has not taken the secret
func jiraCredGet(w io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), jiraCredStoreWait)
	defer cancel()
	s, err := secrets.LoadContext(ctx)
	if err != nil || !jiraConnected(s.Jira) {
		return
	}
	c := *s.Jira
	auth, login, token := "basic", c.Email, c.Token
	if c.AuthKind == "oauth" {
		if c.CloudID == "" {
			return
		}
		if c.Expiry != 0 && time.Now().Add(jiraCredFreshWindow).Unix() >= c.Expiry {
			// A failed renewal still serves a token that has not expired yet: the run may
			// well finish in time, and refusing would fail it for certain.
			if err := jiraRefreshNow(&c); err != nil && time.Now().Unix() >= c.Expiry {
				return
			}
		}
		auth, login, token = "bearer", c.Login, c.AccessToken
	}
	if c.AuthKind == "oauth" {
		jiraBackfillScopes(&c)
	}
	if token == "" || strings.ContainsAny(token+login+c.Site, "\r\n") {
		return
	}
	fmt.Fprintf(w, "site=%s\nserver=%s\nauth=%s\n", c.Site, jiraAPIBase(&c), auth)
	if login != "" {
		fmt.Fprintf(w, "login=%s\n", login)
	}
	if jiraSoftwareScopes(&c) == "missing" {
		fmt.Fprint(w, "software=missing\n")
	}
	fmt.Fprintf(w, "token=%s\n", token)
}
