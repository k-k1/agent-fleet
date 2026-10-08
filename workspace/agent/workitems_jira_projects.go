package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// The project keys of the member's Jira connection, so a key such as PROJ-12 in the mirror can be
// told from UTF-8 and SHA-256 without a cached inbox row of the same project (docs/log/80 §80.25).
//
// Only the keys leave the Agent: no project name, id, lead or URL, and nothing of the credential.
// The list is bounded — jiraProjectMaxKeys keys over at most jiraProjectMaxReads reads — because a large site can
// hold thousands of projects and the answer is cached and shipped to every browser. When the site
// has more (or the walk ends early), `truncated` is true and the keys past the cap simply stay plain text.
const (
	jiraProjectPageSize = 50
	jiraProjectMaxKeys  = 500
	jiraProjectMaxReads = 30
)

// jiraProjectKeyRe is the project part of the key shape the Console links (workitems/refs.ts
// JIRA_REF_SRC). A key outside it could never be matched, and a key outside it must not be
// relayed as data either.
var jiraProjectKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,9}$`)

type jiraProjectsOut struct {
	// Connected is false when the member has no usable Jira connection; Keys is then empty.
	Connected bool `json:"connected"`
	// Keys is never nil.
	Keys      []string `json:"keys"`
	Truncated bool     `json:"truncated"`
}

// handleWorkItemsJiraProjects — POST /work-items/jira-projects. Relayed by the CP like
// /work-items/detail; the Agent reads only the member's own connection.
func handleWorkItemsJiraProjects(w http.ResponseWriter, r *http.Request) {
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	out := jiraProjectsOut{Keys: []string{}}
	if jiraConnected(s.Jira) {
		out.Connected = true
		out.Keys, out.Truncated, err = jiraProjectKeys(s.Jira)
		if err != nil {
			// Words from the status alone, like jiraReferenceError: the upstream text and the
			// OAuth bridge's failure text stay out of the reply.
			msg := "could not read the Jira projects"
			if je, ok := err.(*jiraHTTPError); ok {
				msg = fmt.Sprintf("jira answered %d for the project list", je.code)
			}
			httpx.WriteErr(w, http.StatusBadGateway, "provider_error", msg)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// jiraProjectKeys pages /project/search (which lists the projects the member may browse) in key
// order, so the cut is the same on every read. Jira may answer with fewer values than asked for
// (its own maximum is not promised), so the next page starts after the values actually received,
// never at page*size. The work is bounded three ways: jiraProjectMaxKeys keys kept,
// jiraProjectMaxReads requests, and a page that adds nothing new ends the walk. Any early end that
// leaves the site's list unfinished is reported as truncated.
func jiraProjectKeys(c *secrets.JiraCreds) (keys []string, truncated bool, err error) {
	keys = []string{}
	seen := map[string]bool{}
	start := 0
	for read := 0; read < jiraProjectMaxReads; read++ {
		u := fmt.Sprintf("%s/rest/api/3/project/search?orderBy=key&maxResults=%d&startAt=%d",
			jiraAPIBase(c), jiraProjectPageSize, start)
		body, gerr := jiraGet(c, u)
		if gerr != nil {
			return nil, false, gerr
		}
		var p struct {
			Values []struct {
				Key string `json:"key"`
			} `json:"values"`
			IsLast *bool `json:"isLast"`
			Total  *int  `json:"total"`
		}
		if jerr := json.Unmarshal(body, &p); jerr != nil {
			return nil, false, fmt.Errorf("jira project list: %w", jerr)
		}
		added := 0
		for _, v := range p.Values {
			if !jiraProjectKeyRe.MatchString(v.Key) || seen[v.Key] {
				continue
			}
			if len(keys) >= jiraProjectMaxKeys {
				return keys, true, nil
			}
			seen[v.Key] = true
			keys = append(keys, v.Key)
			added++
		}
		start += len(p.Values)
		last := len(p.Values) == 0 ||
			(p.IsLast != nil && *p.IsLast) ||
			(p.IsLast == nil && p.Total != nil && start >= *p.Total)
		if last {
			return keys, false, nil
		}
		if len(keys) >= jiraProjectMaxKeys {
			return keys, true, nil // the list goes on and the budget is spent
		}
		if added == 0 {
			// A repeated or empty-of-news page: going on would loop on the same answer.
			return keys, true, nil
		}
	}
	return keys, true, nil
}
