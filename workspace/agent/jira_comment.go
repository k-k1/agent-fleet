package main

// `workspace-agent jira-comment issue comment edit|delete KEY ID …` — the two comment
// operations jira-cli lacks, for the jira wrapper (workspace/jira-auth-wrapper.sh), which hands
// over its own argv untouched.
//
// The call is made here, in the process that may hold the token, so the token never reaches the
// wrapper's argv or output. The body is Markdown converted to ADF (jira_adf.go) and sent through
// REST v3, not through jira-cli's wiki converter, so an edit also repairs a comment that
// `comment add` garbled.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

var (
	jiraCommentKeyRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*-[0-9]+$`)
	jiraCommentIDRe  = regexp.MustCompile(`^[0-9]+$`)
)

// jiraCommentArgs is what the jira-cli command line says: the verb, the issue and comment, and
// the body sources.
type jiraCommentArgs struct {
	verb, key, id string
	body          *string // -b / --body
	template      string  // -T / --template; "-" is stdin
}

// parseJiraCommentArgs reads jira's argv: `issue|issues comment edit|delete KEY ID`, with jira's
// global -c / -p (accepted, unused) and the body flags of `comment add`. Boolean flags such as
// --no-input are skipped.
func parseJiraCommentArgs(args []string) (jiraCommentArgs, error) {
	var a jiraCommentArgs
	var pos []string
	valued := map[string]bool{"-b": true, "--body": true, "-T": true, "--template": true,
		"-c": true, "--config": true, "-p": true, "--project": true}
	setFlag := func(name, val string) {
		switch name {
		case "-b", "--body":
			v := val
			a.body = &v
		case "-T", "--template":
			a.template = val
		}
	}
	for i := 0; i < len(args); i++ {
		s := args[i]
		switch {
		case s == "--":
			pos = append(pos, args[i+1:]...)
			i = len(args)
		case valued[s]:
			if i+1 >= len(args) {
				return a, fmt.Errorf("flag %s needs a value", s)
			}
			i++
			setFlag(s, args[i])
		case strings.HasPrefix(s, "--") && strings.Contains(s, "="):
			name, val, _ := strings.Cut(s, "=")
			setFlag(name, val)
		case len(s) > 2 && s[0] == '-' && s[1] != '-' && (s[1] == 'b' || s[1] == 'T'):
			setFlag(s[:2], strings.TrimPrefix(s[2:], "="))
		case strings.HasPrefix(s, "-") && s != "-":
		default:
			pos = append(pos, s)
		}
	}
	if len(pos) != 5 || (pos[0] != "issue" && pos[0] != "issues") || pos[1] != "comment" ||
		(pos[2] != "edit" && pos[2] != "delete") {
		return a, fmt.Errorf("usage: jira issue comment edit|delete KEY ID [-b BODY | -T FILE]")
	}
	a.verb, a.key, a.id = pos[2], pos[3], pos[4]
	if !jiraCommentKeyRe.MatchString(a.key) {
		return a, fmt.Errorf("%q is not an issue key", a.key)
	}
	if !jiraCommentIDRe.MatchString(a.id) {
		return a, fmt.Errorf("%q is not a comment ID (digits; see `jira issue view KEY --raw`)", a.id)
	}
	return a, nil
}

// runJiraComment exits 0 on success, 1 on a failed call and 2 on a bad command line.
func runJiraComment(args []string) {
	var stdin io.Reader
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
		stdin = os.Stdin
	}
	ctx, cancel := context.WithTimeout(context.Background(), jiraCredStoreWait)
	s, err := secrets.LoadContext(ctx)
	cancel()
	var c *secrets.JiraCreds
	if err == nil && jiraConnected(s.Jira) {
		cc := *s.Jira
		c = &cc
	}
	os.Exit(jiraCommentRun(c, args, stdin, os.Stdout, os.Stderr))
}

func jiraCommentRun(c *secrets.JiraCreds, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	a, err := parseJiraCommentArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, "jira:", err)
		return 2
	}
	var md string
	if a.verb == "edit" {
		var err error
		if md, err = jiraCommentBody(a, stdin); err != nil {
			fmt.Fprintln(stderr, "jira:", err)
			return 2
		}
	} else if a.body != nil || a.template != "" {
		fmt.Fprintln(stderr, "jira: comment delete takes no body")
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
	method := "DELETE"
	var payload []byte
	u := jiraAPIBase(c) + "/rest/api/3/issue/" + a.key + "/comment/" + a.id
	if a.verb == "edit" {
		method = "PUT"
		// Jira does not say what an omitted visibility does on update, so the comment's own
		// restriction (role / group, or a JSM internal flag) is read first and sent back as it was.
		// The read also fails early, and in the same words, for a missing comment.
		raw, status, _, err := jiraCommentCall(c, "GET", u+"?expand=properties", nil)
		if err != nil {
			fmt.Fprintln(stderr, "jira:", err)
			return 1
		}
		if status != http.StatusOK {
			fmt.Fprintln(stderr, "jira:", jiraCommentFailure(a, status, raw))
			return 1
		}
		var cur struct {
			Visibility json.RawMessage   `json:"visibility"`
			Properties []json.RawMessage `json:"properties"`
		}
		_ = json.Unmarshal(raw, &cur)
		body := map[string]any{"body": jiraMarkdownADF(md)}
		if v := strings.TrimSpace(string(cur.Visibility)); v != "" && v != "null" {
			body["visibility"] = cur.Visibility
		}
		if len(cur.Properties) > 0 {
			body["properties"] = cur.Properties
		}
		payload, _ = json.Marshal(body)
	}
	raw, status, afterTransport, err := jiraCommentCall(c, method, u, payload)
	if err != nil {
		fmt.Fprintln(stderr, "jira:", err)
		return 1
	}
	if a.verb == "delete" && status == http.StatusNotFound && afterTransport {
		// An attempt cut off in transport may have been applied before the connection died; the
		// retry then finds nothing. Reporting an error here made a delete that worked look failed.
		fmt.Fprintf(stdout, "Comment %s on %s probably already deleted\n", a.id, a.key)
		fmt.Fprintln(stderr, "jira: an earlier attempt failed in transport and the retry got 404, so the delete most likely went through")
		return 0
	}
	if status != http.StatusOK && status != http.StatusNoContent {
		fmt.Fprintln(stderr, "jira:", jiraCommentFailure(a, status, raw))
		return 1
	}
	if a.verb == "delete" {
		fmt.Fprintf(stdout, "Comment %s on %s deleted\n", a.id, a.key)
	} else {
		fmt.Fprintf(stdout, "Comment %s on %s updated: %s/browse/%s?focusedCommentId=%s\n", a.id, a.key, c.Site, a.key, a.id)
	}
	return 0
}

// jiraCommentBody is -b, else -T (`-` is stdin), else piped stdin, as `comment add` takes it.
func jiraCommentBody(a jiraCommentArgs, stdin io.Reader) (string, error) {
	var body string
	switch {
	case a.body != nil:
		body = *a.body
	case a.template == "-" || (a.template == "" && stdin != nil):
		if stdin == nil {
			return "", fmt.Errorf("no body on stdin")
		}
		b, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
		if err != nil {
			return "", err
		}
		body = string(b)
	case a.template != "":
		b, err := os.ReadFile(a.template)
		if err != nil {
			return "", fmt.Errorf("cannot read the template: %v", err)
		}
		body = string(b)
	}
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("an empty comment body (pass -b, -T FILE, or pipe it in)")
	}
	return body, nil
}

// jiraCommentCall is one call with the same renewal rules as jiraPostIssueComment. The bool
// says an attempt failed in transport on the way (see jiraCallResult.afterTransport).
func jiraCommentCall(c *secrets.JiraCreds, method, u string, payload []byte) ([]byte, int, bool, error) {
	if err := jiraEnsureFresh(c); err != nil {
		return nil, 0, false, err
	}
	r := jiraDo(c, method, u, payload)
	if r.err == nil && r.status == http.StatusUnauthorized && c.AuthKind == "oauth" {
		if rerr := jiraRefreshNow(c); rerr == nil {
			after := r.afterTransport
			r = jiraDo(c, method, u, payload)
			r.afterTransport = r.afterTransport || after
		}
	}
	return r.body, r.status, r.afterTransport, r.err
}

// jiraCommentFailure words a refusal in one line, without the token or the request.
func jiraCommentFailure(a jiraCommentArgs, status int, raw []byte) string {
	what := fmt.Sprintf("%s comment %s on %s", a.verb, a.id, a.key)
	switch status {
	case http.StatusUnauthorized:
		return "Jira rejected the credentials (re-connect Jira)"
	case http.StatusForbidden:
		return fmt.Sprintf("Jira refused to %s (403): only the comment's author, or an account with permission to manage comments, may change it, and an OAuth connection needs write:jira-work", what)
	case http.StatusNotFound:
		return fmt.Sprintf("cannot %s (404): there is no comment %s on %s, or the issue is not visible to this account", what, a.id, a.key)
	}
	return fmt.Sprintf("cannot %s (%d): %s", what, status, jiraErrText(raw))
}
