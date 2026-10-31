package mcpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchrule"
)

// mcpToolLinkWorkItem is the session-side face of POST /sessions/{name}/links (#3134): a
// session says which ticket or PR it is working on, reviewing or watching, so the Console can
// show it on the row and in the work item. It links the calling session only — linking
// another session would let one agent decorate rows it does not own.
const mcpToolLinkWorkItem = "link_work_item"

type linkWorkItemArgs struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
	Role     string `json:"role,omitempty"`
	Title    string `json:"title,omitempty"`
	URL      string `json:"url,omitempty"`
	Remove   bool   `json:"remove,omitempty"`
}

func mcpLinkWorkItem(id json.RawMessage, a *linkWorkItemArgs) []byte {
	if !selfReportOnly() {
		return mcpToolErr(id, "link_work_item is only offered to a session")
	}
	owner, err := mcpOwningSession()
	if err != nil {
		return mcpToolErr(id, "cannot tell which session is calling: "+err.Error())
	}
	body, _ := json.Marshal(a)
	out, err := agentDo(http.MethodPost, "/sessions/"+url.PathEscape(owner)+"/links", body)
	if err != nil {
		var he *agentHTTPError
		if errors.As(err, &he) && he.code() == "" && he.StatusCode == http.StatusNotFound {
			return mcpToolErr(id, "this Workspace Agent predates work-item links; nothing was recorded")
		}
		return mcpToolErr(id, "link_work_item failed: "+agentErrDetail(err))
	}
	return mcpTextResult(id, out)
}

// linkBranchItem records the item a branch was just named after as the caller's working link.
// Best effort and silent: the name is what the caller asked for, and a link that cannot be
// written (an older Agent, a key without a repository) must not turn it into an error.
func linkBranchItem(owner string, item *branchrule.Item) {
	if owner == "" || item == nil || strings.TrimSpace(item.Key) == "" {
		return
	}
	body, _ := json.Marshal(linkWorkItemArgs{Provider: itemProvider(item), Key: item.Key, Role: "working", Title: item.Title})
	// Short, because branch_name waits for it and the name is already in hand.
	_, _ = agentDoTimeout(http.MethodPost, "/sessions/"+url.PathEscape(owner)+"/links", body, linkBranchTimeout)
}

const linkBranchTimeout = 3 * time.Second

// jiraKeyShape is a Jira key as an agent may type it; NormalizeLinkKey upper-cases it.
var jiraKeyShape = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*-[1-9][0-9]*$`)

// itemProvider is the item's provider, or, when branch_name was given none, the one its key's
// shape names: PROJ-12 is Jira, anything else is taken to be GitHub.
func itemProvider(item *branchrule.Item) string {
	if p := strings.TrimSpace(item.Provider); p != "" {
		return p
	}
	if jiraKeyShape.MatchString(strings.TrimSpace(item.Key)) {
		return "jira"
	}
	return "github"
}

func linkWorkItemSchema() map[string]any {
	return map[string]any{
		"name": mcpToolLinkWorkItem,
		"description": "Agent Fleet: link your session to an issue, ticket or pull request so the Console shows it on your row and in that item. " +
			"Call it when you start working on, reviewing or watching an item you were not launched for. " +
			"branch_name with an item and the PR of your branch are linked for you. remove: true unlinks.",
		"inputSchema": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"provider": map[string]any{"type": "string", "enum": []string{"github", "jira", "bitbucket"}},
				"key":      map[string]any{"type": "string", "description": "owner/repo#45, or #45 in your own repository, or PROJ-12"},
				"role":     map[string]any{"type": "string", "enum": []string{"working", "reviewing", "watching", "created"}, "description": "default working"},
				"title":    map[string]any{"type": "string"},
				"url":      map[string]any{"type": "string"},
				"remove":   map[string]any{"type": "boolean"},
			},
			"required": []string{"provider", "key"},
		},
	}
}
