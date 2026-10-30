package jirax

// `workspace-agent jira-issue-types KEY` — the project's issue types, as the config fragment
// `jira issue create` reads, for the jira wrapper (workspace/jira-auth-wrapper.sh).
//
// jira-cli's `issue create` takes its issue types from the config only, and the one command
// that writes them (`jira init`) asks the Agile board API first, which an OAuth connection
// without the Jira Software scopes gets a 401 for. GET /rest/api/2/issue/createmeta is
// classic scope (read:jira-work), so this fetches the types without it. The call is made
// here, inside the process that may hold the token: stdout carries only non-secret data, so
// the wrapper can write it to a file.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// jiraArgProjectKeyRe is loose on purpose (lower case, any length): the key comes from the
// user's argv and only has to be safe to put in a query string and a file name.
var jiraArgProjectKeyRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// RunIssueTypes prints the fragment, or nothing and exit status 1 on any failure: the
// wrapper then runs jira as it would have without this.
func RunIssueTypes(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: workspace-agent jira-issue-types PROJECT-KEY")
		os.Exit(2)
	}
	if err := jiraIssueTypesGet(os.Stdout, args[0]); err != nil {
		fmt.Fprintln(os.Stderr, "jira-issue-types:", err)
		os.Exit(1)
	}
}

func jiraIssueTypesGet(w io.Writer, key string) error {
	if !jiraArgProjectKeyRe.MatchString(key) {
		return fmt.Errorf("not a project key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), jiraCredStoreWait)
	defer cancel()
	s, err := secrets.LoadContext(ctx)
	if err != nil || !Connected(s.Jira) {
		return fmt.Errorf("no Jira connection")
	}
	c := *s.Jira
	if c.AuthKind == "oauth" && c.CloudID == "" {
		return fmt.Errorf("no Jira site selected")
	}
	body, err := Get(&c, APIBase(&c)+"/rest/api/2/issue/createmeta?projectKeys="+url.QueryEscape(key))
	if err != nil {
		return err
	}
	out, err := jiraIssueTypesYAML(body)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, out)
	return err
}

// jiraIssueTypesYAML maps a createmeta body onto jira-cli's `issue.types` shape: id a string,
// subtask a bool (the CLI asserts both types), handle the untranslated name. Strings go through
// the JSON encoder, whose output is a valid YAML double-quoted scalar.
func jiraIssueTypesYAML(body []byte) (string, error) {
	var meta struct {
		Projects []struct {
			IssueTypes []struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Handle  string `json:"untranslatedName"`
				Subtask bool   `json:"subtask"`
			} `json:"issuetypes"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		return "", err
	}
	if len(meta.Projects) == 0 || len(meta.Projects[0].IssueTypes) == 0 {
		// createmeta answers 200 with an empty list for a project the account cannot create in
		// (or that does not exist); writing an empty list would make jira's error worse.
		return "", fmt.Errorf("no issue types in createmeta (unknown project, or no permission to create issues)")
	}
	q := func(v string) string { b, _ := json.Marshal(v); return string(b) }
	var sb strings.Builder
	sb.WriteString("issue:\n  types:\n")
	for _, it := range meta.Projects[0].IssueTypes {
		if it.ID == "" || it.Name == "" {
			continue
		}
		h := it.Handle
		if h == "" {
			h = it.Name
		}
		fmt.Fprintf(&sb, "    - id: %s\n      name: %s\n      handle: %s\n      subtask: %t\n", q(it.ID), q(it.Name), q(h), it.Subtask)
	}
	if !strings.Contains(sb.String(), "- id:") {
		return "", fmt.Errorf("no usable issue types in createmeta")
	}
	return sb.String(), nil
}
