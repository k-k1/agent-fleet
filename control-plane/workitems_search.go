package main

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Searching past the rail's cut (#1095). The rail holds one page per saved query, and its
// filter box only narrows those rows — so a ticket past the page is "not there", and the box
// confirms it. This asks the tracker instead: each enabled saved query, narrowed by what was
// typed, resolved live through the Agent's /work-items/fetch (the Agent holds the tokens).
//
// The results are NOT cached. They answer one question typed into one box, and writing them
// into work_item_cache would make the next refresh silently take them away again. Only on an
// explicit press, and only while the Workspace runs — never started for it (ADR 0061
// decision 1), and GitHub's search budget is 30 calls a minute, which a per-keystroke search
// across ten queries would spend in one word.

const workItemSearchMaxNeedle = 200

type workItemSearchWire struct {
	Items []workItemDTO `json:"items"`
	// Errors carries per-query failures ({queryId, message}); Skipped the queries whose
	// provider cannot be narrowed from here (Bitbucket's filter language has no free text).
	Errors  []workItemSearchErr `json:"errors"`
	Skipped []string            `json:"skipped"`
}

type workItemSearchErr struct {
	QueryID string `json:"queryId"`
	Message string `json:"message"`
}

func (a workItemsAPI) search(w http.ResponseWriter, r *http.Request, res *resolved) {
	var in struct {
		Q string `json:"q"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	needle := strings.TrimSpace(in.Q)
	if needle == "" || len(needle) > workItemSearchMaxNeedle {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "q is required (at most 200 bytes)"})
		return
	}
	ctx := r.Context()
	if res.rt.State(ctx) != "running" {
		writeAPIErr(w, &apiError{http.StatusConflict, "workspace_stopped",
			"start the workspace to search the tracker (the tracker credentials live in it)"})
		return
	}
	queries, err := a.store.ListWorkItemQueries(ctx, res.mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := workItemSearchWire{Items: []workItemDTO{}, Errors: []workItemSearchErr{}, Skipped: []string{}}
	narrowed := make([]store.WorkItemQuery, 0, len(queries))
	for _, q := range queries {
		if !q.Enabled {
			continue
		}
		nq, ok := narrowWorkItemQuery(q.Provider, q.Query, needle)
		if !ok {
			out.Skipped = append(out.Skipped, q.ID)
			continue
		}
		q.Query = nq
		narrowed = append(narrowed, q)
		if len(narrowed) >= workItemsMaxQueries {
			break
		}
	}
	if len(narrowed) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}
	rows, errs, _, err := fetchWorkItemsFromAgent(ctx, res.rt, narrowed)
	if err != nil {
		writeAPIErr(w, &apiError{http.StatusBadGateway, "agent_unreachable", err.Error()})
		return
	}
	seen := map[string]bool{}
	for _, q := range narrowed {
		if msg := errs[q.ID]; msg != "" {
			out.Errors = append(out.Errors, workItemSearchErr{QueryID: q.ID, Message: msg})
			continue
		}
		for _, it := range rows[q.ID] {
			// One row per ticket, as on the rail: two saved queries often match the same one.
			id := it.Provider + ":" + it.Key
			if seen[id] {
				continue
			}
			seen[id] = true
			it.ID = id
			it.QueryID = q.ID
			out.Items = append(out.Items, workItemToDTO(it))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// narrowWorkItemQuery adds what was typed to a saved query in the provider's own language.
// ok=false when the provider cannot be narrowed by free text.
func narrowWorkItemQuery(provider, query, needle string) (string, bool) {
	switch strings.TrimSpace(provider) {
	case "", "github":
		// "#1028" is how people write an issue number, and GitHub's search matches the bare
		// number (measured: `is:open involves:@me 1028` finds #1028).
		n := strings.TrimPrefix(needle, "#")
		if n == "" {
			return "", false
		}
		// Terms are ANDed with the saved query. Wrap a query that uses boolean operators, or the
		// added term would bind to its last operand only. Not every query, because parentheses
		// need advanced search, which an Agent from before it answers with 422.
		if githubBoolOps.MatchString(query) {
			return "(" + query + ") " + n, true
		}
		return query + " " + n, true
	case "jira":
		base := strings.TrimSpace(query)
		// ORDER BY must stay at the very end of JQL, so the added clause goes before it.
		order := ""
		if loc := jiraOrderBy.FindStringIndex(base); loc != nil {
			base, order = strings.TrimSpace(base[:loc[0]]), " "+base[loc[0]:]
		}
		var cond string
		if jiraIssueKey.MatchString(needle) {
			// text ~ does not search issue keys, and an issue key is what people paste.
			cond = `key = "` + strings.ToUpper(needle) + `"`
		} else {
			cond = `text ~ "` + jqlEscape(needle) + `"`
		}
		if base == "" {
			return cond + order, true
		}
		return "(" + base + ") AND " + cond + order, true
	}
	return "", false
}

var (
	githubBoolOps = regexp.MustCompile(`\b(OR|AND|NOT)\b|[()]`)
	jiraOrderBy   = regexp.MustCompile(`(?i)\border\s+by\b`)
	jiraIssueKey  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*-[0-9]+$`)
)

// jqlEscape makes s safe inside a double-quoted JQL string.
func jqlEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}
