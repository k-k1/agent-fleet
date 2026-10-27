package main

// `workspace-agent jira-description KEY [--skip-notify]` — sets an issue's description from the
// Markdown on stdin, for the jira wrapper's `issue create -b/-T` and `issue edit -b`.
//
// jira-cli sends a description as v2 wiki markup, where `{{code}}` and `*bold*` only take effect
// at a word boundary, and Japanese text or full-width brackets are none: `認証（`/api/auth`）`
// showed up as literal braces. The wrapper therefore runs jira-cli without the body and sets it
// here as ADF through REST v3 (jira_adf.go). The token stays in this process, as for jira-comment.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// runJiraDescription exits 0 on success, 1 on a failed call and 2 on a bad command line.
func runJiraDescription(args []string) {
	ctx, cancel := context.WithTimeout(context.Background(), jiraCredStoreWait)
	s, err := secrets.LoadContext(ctx)
	cancel()
	var c *secrets.JiraCreds
	if err == nil && jiraConnected(s.Jira) {
		cc := *s.Jira
		c = &cc
	}
	os.Exit(jiraDescriptionRun(c, args, os.Stdin, os.Stderr))
}

func jiraDescriptionRun(c *secrets.JiraCreds, args []string, stdin io.Reader, stderr io.Writer) int {
	var key string
	notify := true
	for _, a := range args {
		switch {
		case a == "--skip-notify":
			notify = false
		case key == "" && jiraCommentKeyRe.MatchString(a):
			key = a
		default:
			fmt.Fprintln(stderr, "jira: usage: workspace-agent jira-description KEY [--skip-notify] < BODY")
			return 2
		}
	}
	if key == "" {
		fmt.Fprintln(stderr, "jira: usage: workspace-agent jira-description KEY [--skip-notify] < BODY")
		return 2
	}
	b, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		fmt.Fprintln(stderr, "jira: cannot read the description:", err)
		return 2
	}
	if strings.TrimSpace(string(b)) == "" {
		fmt.Fprintln(stderr, "jira: an empty description")
		return 2
	}
	if c == nil {
		fmt.Fprintln(stderr, "jira: no Jira connection (connect Jira in Settings)")
		return 1
	}
	if c.AuthKind == "oauth" && c.CloudID == "" {
		fmt.Fprintln(stderr, "jira: no Jira site selected for the connection")
		return 1
	}
	payload, _ := json.Marshal(map[string]any{"fields": map[string]any{"description": jiraMarkdownADF(string(b))}})
	u := jiraAPIBase(c) + "/rest/api/3/issue/" + key
	if !notify {
		u += "?notifyUsers=false"
	}
	raw, status, _, err := jiraCommentCall(c, "PUT", u, payload)
	if err != nil {
		fmt.Fprintln(stderr, "jira:", err)
		return 1
	}
	if status == http.StatusOK || status == http.StatusNoContent {
		return 0
	}
	switch status {
	case http.StatusUnauthorized:
		fmt.Fprintln(stderr, "jira: Jira rejected the credentials (re-connect Jira)")
	case http.StatusForbidden:
		fmt.Fprintf(stderr, "jira: Jira refused to set the description of %s (403): the account may lack the Edit Issues permission (or, with --skip-notify, the right to suppress notifications), and an OAuth connection needs write:jira-work\n", key)
	case http.StatusNotFound:
		fmt.Fprintf(stderr, "jira: cannot set the description of %s (404): there is no such issue, or it is not visible to this account\n", key)
	default:
		fmt.Fprintf(stderr, "jira: cannot set the description of %s (%d): %s\n", key, status, jiraErrText(raw))
	}
	return 1
}
