package jirax

// `workspace-agent jira-description-required PROJECT TYPE` — whether `jira issue create` in that
// project and issue type must carry a description, for the jira wrapper.
//
// The wrapper creates an issue without its body and sets the description afterwards as ADF
// (jira_description.go). A project whose field configuration requires a description refuses
// that create. jira-cli prints the refusal without its HTTP status, so the wrapper cannot tell
// "refused, nothing created" from a failure after the issue was made, and must not re-send the
// create on that text. It asks here first instead, and sends the body with the create (through
// jira-cli's own conversion) when the answer is "required".
//
// Prints "required" or "optional" and exits 0; exits 1, printing nothing, when it cannot tell
// (the wrapper then keeps the ADF path).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

func RunDescriptionRequired(args []string) {
	if len(args) != 2 || !jiraArgProjectKeyRe.MatchString(args[0]) || strings.TrimSpace(args[1]) == "" {
		fmt.Fprintln(os.Stderr, "usage: workspace-agent jira-description-required PROJECT-KEY ISSUE-TYPE")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), jiraCredStoreWait)
	s, err := secrets.LoadContext(ctx)
	cancel()
	if err != nil || !Connected(s.Jira) {
		fmt.Fprintln(os.Stderr, "jira-description-required: no Jira connection")
		os.Exit(1)
	}
	c := *s.Jira
	required, err := jiraDescriptionRequired(&c, args[0], args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "jira-description-required:", err)
		os.Exit(1)
	}
	if required {
		fmt.Println("required")
	} else {
		fmt.Println("optional")
	}
}

// jiraDescriptionRequired reads the createmeta of one project and issue type (the paged
// endpoints that replaced `createmeta?expand=projects.issuetypes.fields`). The type is matched
// by name or untranslated name, case-insensitively, as jira-cli sends the name.
func jiraDescriptionRequired(c *secrets.JiraCreds, project, issueType string) (bool, error) {
	if c.AuthKind == "oauth" && c.CloudID == "" {
		return false, fmt.Errorf("no Jira site selected")
	}
	base := APIBase(c) + "/rest/api/2/issue/createmeta/" + url.PathEscape(project) + "/issuetypes"
	typeID := ""
	err := jiraEachPage(c, base, func(raw json.RawMessage) bool {
		var it struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Handle string `json:"untranslatedName"`
		}
		if json.Unmarshal(raw, &it) == nil && it.ID != "" &&
			(strings.EqualFold(it.Name, issueType) || strings.EqualFold(it.Handle, issueType)) {
			typeID = it.ID
			return false
		}
		return true
	}, "issueTypes")
	if err != nil {
		return false, err
	}
	if typeID == "" {
		return false, fmt.Errorf("no issue type %q in project %s", issueType, project)
	}
	// A description that is not on the create screen at all is not required there.
	required := false
	err = jiraEachPage(c, base+"/"+url.PathEscape(typeID), func(raw json.RawMessage) bool {
		var f struct {
			FieldID  string `json:"fieldId"`
			Key      string `json:"key"`
			Required bool   `json:"required"`
		}
		if json.Unmarshal(raw, &f) == nil && (f.FieldID == "description" || f.Key == "description") {
			required = f.Required
			return false
		}
		return true
	}, "fields")
	if err != nil {
		return false, err
	}
	return required, nil
}

// jiraEachPage walks a paged createmeta list (`startAt` / `maxResults` / `total`), calling fn
// for each element until it returns false. The list is under listKey on Cloud and under
// "values" on Data Center.
func jiraEachPage(c *secrets.JiraCreds, u string, fn func(json.RawMessage) bool, listKey string) error {
	start := 0
	for page := 0; page < 20; page++ {
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		body, err := Get(c, u+sep+"startAt="+strconv.Itoa(start)+"&maxResults=100")
		if err != nil {
			return err
		}
		var p map[string]json.RawMessage
		if err := json.Unmarshal(body, &p); err != nil {
			return err
		}
		var items []json.RawMessage
		raw := p[listKey]
		if len(raw) == 0 {
			raw = p["values"]
		}
		if err := json.Unmarshal(raw, &items); err != nil {
			return fmt.Errorf("unexpected createmeta answer")
		}
		for _, it := range items {
			if !fn(it) {
				return nil
			}
		}
		var meta struct {
			Total  *int  `json:"total"`
			IsLast *bool `json:"isLast"`
		}
		_ = json.Unmarshal(body, &meta)
		start += len(items)
		if len(items) == 0 || (meta.IsLast != nil && *meta.IsLast) || (meta.Total != nil && start >= *meta.Total) {
			return nil
		}
	}
	return nil
}
