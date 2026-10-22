package main

// `workspace-agent jira-find-created KEY SUMMARY` — the issues the connection's user created in
// project KEY in the last 15 minutes whose summary is exactly SUMMARY, for the jira wrapper
// (workspace/jira-auth-wrapper.sh) after a timed-out `issue create`.
//
// A timed-out create is not re-run because it may have been applied; this lets the wrapper name
// the candidates so the caller can tell "created" from "lost" without a second create. Read-only,
// and the token stays in this process: stdout is "KEY <browse URL>" per line, nothing secret.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// jiraFindWindow is how far back a created issue still counts as a candidate; it is also the
// literal the wrapper's "no matching issue found in the last 15 minutes" line quotes.
const jiraFindWindow = "-15m"

// jiraFindDeadline bounds the whole lookup, store wait and retries included: the wrapper has
// already spent its create timeout and must not hang a second time.
var jiraFindDeadline = 15 * time.Second

// jiraFindMax caps the rows read. A user rarely creates more than a few issues in 15 minutes;
// beyond the cap the exact-summary filter would silently miss, so the cap is generous.
const jiraFindMax = 50

// runJiraFindCreated exits 0 with zero or more candidate lines, 1 when the lookup failed
// (reason on stderr), 2 on usage.
func runJiraFindCreated(args []string) {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: workspace-agent jira-find-created PROJECT-KEY SUMMARY")
		os.Exit(2)
	}
	if err := jiraFindCreatedRun(os.Stdout, args[0], args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "jira-find-created:", err)
		os.Exit(1)
	}
}

// jiraFindCreatedRun runs the lookup under jiraFindDeadline. The REST helper takes no context,
// so the deadline is enforced by abandoning the call; the process exits right after.
func jiraFindCreatedRun(w io.Writer, key, summary string) error {
	if !jiraArgProjectKeyRe.MatchString(key) {
		return fmt.Errorf("not a project key")
	}
	if strings.TrimSpace(summary) == "" {
		return fmt.Errorf("empty summary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), jiraFindDeadline)
	defer cancel()
	type result struct {
		lines []string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		lines, err := jiraFindCreated(ctx, key, summary)
		done <- result{lines, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			return r.err
		}
		for _, l := range r.lines {
			if _, err := fmt.Fprintln(w, l); err != nil {
				return err
			}
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("no answer within %s", jiraFindDeadline)
	}
}

func jiraFindCreated(ctx context.Context, key, summary string) ([]string, error) {
	s, err := secrets.LoadContext(ctx)
	if err != nil || !jiraConnected(s.Jira) {
		return nil, fmt.Errorf("no Jira connection")
	}
	c := *s.Jira
	if c.AuthKind == "oauth" && c.CloudID == "" {
		return nil, fmt.Errorf("no Jira site selected")
	}
	body, err := jiraFindSearch(&c, jiraFindJQL(key, summary, true))
	if je, ok := err.(*jiraHTTPError); ok && je.code == 400 {
		// `~` is a text-index query and Jira's parser refuses some summaries (Lucene syntax
		// characters); the window and the reporter already narrow the set, so ask without it.
		body, err = jiraFindSearch(&c, jiraFindJQL(key, summary, false))
	}
	if err != nil {
		return nil, err
	}
	return jiraFindMatches(body, c.Site, summary)
}

// jiraFindJQL: project, reporter and window always; the fuzzy summary clause only when asked.
func jiraFindJQL(key, summary string, withSummary bool) string {
	q := `project = "` + jiraJQLString(key) + `" AND reporter = currentUser() AND created >= ` + jiraFindWindow
	if withSummary {
		q += ` AND summary ~ "` + jiraJQLString(summary) + `"`
	}
	return q + " ORDER BY created DESC"
}

// jiraJQLString escapes a value for a double-quoted JQL string: backslash and quote, and the
// control characters a summary pasted from elsewhere may carry.
func jiraJQLString(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(s)
}

// jiraFindSearch uses the same two endpoints as jiraSearchWorkItems and falls back on 404/410.
func jiraFindSearch(c *secrets.JiraCreds, jql string) ([]byte, error) {
	q := "?jql=" + url.QueryEscape(jql) + "&maxResults=" + fmt.Sprint(jiraFindMax) + "&fields=summary"
	base := jiraAPIBase(c)
	body, err := jiraGet(c, base+"/rest/api/3/search/jql"+q)
	if isJiraNotFound(err) {
		body, err = jiraGet(c, base+"/rest/api/3/search"+q)
	}
	return body, err
}

// jiraFindMatches keeps the issues whose summary equals the wanted one exactly (surrounding
// whitespace aside, which Jira trims) and renders "KEY <site>/browse/KEY".
func jiraFindMatches(body []byte, site, summary string) ([]string, error) {
	var jr struct {
		Issues []struct {
			Key    string `json:"key"`
			Fields struct {
				Summary string `json:"summary"`
			} `json:"fields"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(body, &jr); err != nil {
		return nil, fmt.Errorf("unreadable search answer")
	}
	want := strings.TrimSpace(summary)
	var out []string
	for _, is := range jr.Issues {
		if is.Key != "" && strings.TrimSpace(is.Fields.Summary) == want {
			out = append(out, is.Key+" "+strings.TrimRight(site, "/")+"/browse/"+is.Key)
		}
	}
	return out, nil
}
