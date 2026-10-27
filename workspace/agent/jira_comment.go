package main

// `workspace-agent jira-comment issue comment add|edit|delete KEY …` — the comment operations of
// the jira wrapper (workspace/jira-auth-wrapper.sh), which hands over its own argv untouched.
// jira-cli lacks edit and delete; add is taken over because jira-cli sends its body as v2 wiki
// markup, where `{{code}}` and `*bold*` next to Japanese text stay literal.
//
// The call is made here, in the process that may hold the token, so the token never reaches the
// wrapper's argv or output. The body is Markdown converted to ADF (jira_adf.go) and sent through
// REST v3, not through jira-cli's wiki converter, so an edit also repairs a comment that an older
// `comment add` garbled.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
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
	body          *string // -b / --body, or add's positional body
	template      string  // -T / --template; "-" is stdin
	internal      bool    // add --internal (a JSM internal comment)
}

// parseJiraCommentArgs reads jira's argv: `issue|issues comment add KEY [BODY]` or
// `issue|issues comment edit|delete KEY ID`, with jira's global -c / -p (accepted, unused), the
// body flags, --no-input and add's --internal. Any other flag, or a boolean value
// strconv.ParseBool refuses, is an error, as cobra makes it: a mistyped `--internl` posted
// publicly would be a comment nobody meant to publish. Help, --web and --debug are jira-cli's
// (the wrapper never sends them here in effect); only their false values are accepted.
func parseJiraCommentArgs(args []string) (jiraCommentArgs, error) {
	var a jiraCommentArgs
	var pos []string
	valued := map[string]bool{"-b": true, "--body": true, "-T": true, "--template": true,
		"-c": true, "--config": true, "-p": true, "--project": true}
	boolean := map[string]bool{"--no-input": true, "--internal": true, "--web": true, "--debug": true, "--help": true, "-h": true}
	seen := map[string]bool{}
	jiraCLIOnly := map[string]bool{}
	setFlag := func(name, val string) {
		seen[name] = true
		switch name {
		case "-b", "--body":
			v := val
			a.body = &v
		case "-T", "--template":
			a.template = val
		}
	}
	setBool := func(name, val string) error {
		b, err := strconv.ParseBool(val)
		if err != nil {
			return fmt.Errorf("invalid argument %q for %q flag", val, name)
		}
		switch name {
		case "--internal":
			a.internal = b
		case "--web", "--debug", "--help", "-h":
			// The last value wins, as in pflag: judged once argv has been read.
			key := strings.TrimLeft(name, "-")
			if key == "h" {
				key = "help"
			}
			jiraCLIOnly[key] = b
		}
		seen[name] = true
		return nil
	}
	for i := 0; i < len(args); i++ {
		s := args[i]
		switch {
		case s == "--":
			pos = append(pos, args[i+1:]...)
			i = len(args)
		case valued[s]:
			if i+1 >= len(args) {
				return a, fmt.Errorf("flag needs an argument: %s", s)
			}
			i++
			setFlag(s, args[i])
		case boolean[s]:
			if err := setBool(s, "true"); err != nil {
				return a, err
			}
		case strings.HasPrefix(s, "--") && strings.Contains(s, "="):
			name, val, _ := strings.Cut(s, "=")
			switch {
			case valued[name]:
				setFlag(name, val)
			case boolean[name]:
				if err := setBool(name, val); err != nil {
					return a, err
				}
			default:
				return a, fmt.Errorf("unknown flag: %s", name)
			}
		case strings.HasPrefix(s, "-h="):
			if err := setBool("-h", s[3:]); err != nil {
				return a, err
			}
		case len(s) > 2 && s[0] == '-' && s[1] != '-' && valued[s[:2]]:
			setFlag(s[:2], strings.TrimPrefix(s[2:], "="))
		case strings.HasPrefix(s, "-") && s != "-":
			return a, fmt.Errorf("unknown flag: %s", s)
		default:
			pos = append(pos, s)
		}
	}
	for _, name := range []string{"help", "web", "debug"} {
		if jiraCLIOnly[name] {
			return a, fmt.Errorf("--%s is handled by jira-cli, not here", name)
		}
	}
	isAdd := len(pos) >= 3 && pos[2] == "add"
	if isAdd && (seen["-b"] || seen["--body"]) {
		return a, fmt.Errorf("unknown flag: --body (comment add takes the body as an argument, -T FILE or stdin)")
	}
	if !isAdd && seen["--internal"] {
		return a, fmt.Errorf("unknown flag: --internal")
	}
	if len(pos) >= 3 && (pos[0] == "issue" || pos[0] == "issues") && (pos[1] == "comment" || pos[1] == "comments") &&
		pos[2] == "add" && (len(pos) == 4 || len(pos) == 5) {
		a.verb, a.key = "add", pos[3]
		// jira-cli: a positional body wins over -T, and an empty one is no body.
		if len(pos) == 5 && pos[4] != "" {
			a.body = &pos[4]
		}
		if !jiraCommentKeyRe.MatchString(a.key) {
			return a, fmt.Errorf("%q is not an issue key", a.key)
		}
		return a, nil
	}
	if len(pos) != 5 || (pos[0] != "issue" && pos[0] != "issues") || pos[1] != "comment" ||
		(pos[2] != "edit" && pos[2] != "delete") {
		return a, fmt.Errorf("usage: jira issue comment add KEY [BODY] | edit KEY ID [-b BODY | -T FILE] | delete KEY ID")
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
	// Implicit stdin is a body only when it is not a terminal or another character device, as
	// jira-cli reads it; an explicit `-T -` reads it whatever it is (a terminal until EOF).
	var stdin io.Reader
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
		stdin = os.Stdin
	} else if a, err := parseJiraCommentArgs(args); err == nil && a.template == "-" {
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
	if a.verb == "edit" || a.verb == "add" {
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
	if a.verb == "add" {
		return jiraCommentAdd(c, a, md, stdout, stderr)
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

// jiraCommentAdd posts a new comment. A POST is never re-sent (jira_retry.go), so a transport
// failure says the comment may exist instead of inviting a re-run that would post it twice.
func jiraCommentAdd(c *secrets.JiraCreds, a jiraCommentArgs, md string, stdout, stderr io.Writer) int {
	body := map[string]any{"body": jiraMarkdownADF(md)}
	if a.internal {
		body["properties"] = []any{map[string]any{"key": "sd.public.comment", "value": map[string]any{"internal": true}}}
	}
	payload, _ := json.Marshal(body)
	raw, status, _, err := jiraCommentCall(c, "POST", jiraAPIBase(c)+"/rest/api/3/issue/"+a.key+"/comment", payload)
	if err != nil {
		fmt.Fprintf(stderr, "jira: %v; the comment may have been added - check %s before running it again\n", err, a.key)
		return 1
	}
	if status != http.StatusCreated && status != http.StatusOK {
		fmt.Fprintln(stderr, "jira:", jiraCommentFailure(a, status, raw))
		return 1
	}
	var out struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &out)
	u := c.Site + "/browse/" + a.key
	if out.ID != "" {
		u += "?focusedCommentId=" + out.ID
	}
	fmt.Fprintf(stdout, "Comment added to issue %q\n%s\n", a.key, u)
	return 0
}

// jiraCommentBody is -b (or add's positional body), else -T (`-` is stdin), else piped stdin, as
// `comment add` takes it.
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
	if a.verb == "add" {
		what = "add a comment to " + a.key
		switch status {
		case http.StatusForbidden:
			return fmt.Sprintf("Jira refused to %s (403): the account may lack the Add Comments permission, and an OAuth connection needs write:jira-work", what)
		case http.StatusNotFound:
			return fmt.Sprintf("cannot %s (404): there is no issue %s, or it is not visible to this account", what, a.key)
		}
	}
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
