package workitems

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/jirax"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// jiraSearchWorkItems resolves one saved JQL query into rail rows.
//
// Two endpoints, on purpose. Atlassian replaced the classic
// GET /rest/api/3/search with /rest/api/3/search/jql (token paging instead of
// startAt/total), and which one a given site answers depends on when it was migrated.
// We try the newer path and fall back on 404/410 — the issue shape is identical, so the
// mapping below is shared. Guessing wrong in either direction would make the whole
// provider look broken.
func jiraSearchWorkItems(c *secrets.JiraCreds, queryID, jql string) ([]workItemOut, error) {
	fields := "summary,status,assignee,labels,updated,issuetype"
	q := "?jql=" + url.QueryEscape(jiraOrderedJQL(jql)) + "&maxResults=" + fmt.Sprint(workItemFetchPerQuery) + "&fields=" + url.QueryEscape(fields)
	base := jirax.APIBase(c)
	body, err := jirax.Get(c, base+"/rest/api/3/search/jql"+q)
	if jirax.IsNotFound(err) {
		body, err = jirax.Get(c, base+"/rest/api/3/search"+q)
	}
	if err != nil {
		return nil, err
	}
	return parseJiraSearchIssues(body, c.Site, queryID)
}

// jiraOrderedJQL appends `ORDER BY updated DESC` to a query that does not order itself
// (docs/log/80 §80.18.6).
//
// Because the fetch is capped at workItemFetchPerQuery, an unordered JQL means WHICH
// 50 rows survive is up to Jira. The rail says "the newest N" — the GitHub adapter passes
// sort=updated&order=desc for exactly this reason — so an arbitrary cut would make the
// rail lie. A user-written ORDER BY is left alone: they said what they wanted.
func jiraOrderedJQL(jql string) string {
	q := strings.TrimSpace(jql)
	if q == "" {
		return q
	}
	// "ORDER BY" may only appear at the end of a JQL query, so absorbing case and
	// whitespace variation is enough to find it.
	if jiraOrderBy.MatchString(q) {
		return q
	}
	return q + " ORDER BY updated DESC"
}

var jiraOrderBy = regexp.MustCompile(`(?i)\border\s+by\b`)

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
				Labels    []string `json:"labels"`
				IssueType *struct {
					Name string `json:"name"`
				} `json:"issuetype"`
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
		// An issue without fields.labels leaves this nil, and a nil slice marshals to JSON
		// null, which the receiver cannot treat as an array (this exact shape once crashed
		// the Console).
		labels := is.Fields.Labels
		if labels == nil {
			labels = []string{}
		}
		typ := ""
		if is.Fields.IssueType != nil {
			typ = is.Fields.IssueType.Name
		}
		out = append(out, workItemOut{
			QueryID: queryID, Provider: "jira", Kind: "issue", Key: is.Key,
			Title:    is.Fields.Summary,
			State:    normalizeJiraState(is.Fields.Status.StatusCategory.Key),
			URL:      site + "/browse/" + is.Key,
			Assignee: assignee, Labels: labels, Type: typ, LabelColors: map[string]string{},
			// Jira has no repository. The launch target comes from the query's repoHint,
			// which is the project-to-working-copy mapping.
			Repo: "", UpdatedAt: jiraTimeToRFC3339(is.Fields.Updated),
		})
	}
	return out, nil
}

// normalizeJiraState maps Jira onto the shared vocabulary via the STATUS CATEGORY, not
// the status name. Every Jira project renames its statuses ("In review", "Awaiting QA"),
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
