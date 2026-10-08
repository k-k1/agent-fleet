package mcpx

// AF-owned agent memory (ADR 0108): the session-side face of /agents/memory/entries. The
// store, its lock and its history live in the Agent, so every kind's MCP server goes through
// the same loopback routes and two writers can never race on the files.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	mcpToolMemoryIndex  = "memory_index"
	mcpToolMemorySearch = "memory_search"
	mcpToolMemoryRead   = "memory_read"
	mcpToolMemorySave   = "memory_save"
	mcpToolMemoryForget = "memory_forget"
)

// mcpMemoryEntry is the part of the Agent's answer these tools print.
type mcpMemoryEntry struct {
	Name          string   `json:"name"`
	Scope         string   `json:"scope"`
	Description   string   `json:"description"`
	Type          string   `json:"type"`
	Kinds         []string `json:"kinds"`
	Revision      int      `json:"revision"`
	AuthorKind    string   `json:"authorKind"`
	AuthorSession string   `json:"authorSession"`
	Updated       string   `json:"updated"`
	Body          string   `json:"body"`
	Snippets      []string `json:"snippets"`
}

type mcpMemoryProject struct {
	Display string `json:"display"`
}

// mcpAgentMemoryEnabled advertises the memory tools under `--self-report --agent-memory`, the
// user's switch (ui-prefs agentMemory, default off). The Agent re-checks the switch on every call
// (memoryx.AgentMemoryEnabled), so turning it off refuses sessions launched while it was on.
var mcpAgentMemoryEnabled bool

// mcpMemoryCall dispatches the five memory tools.
func mcpMemoryCall(id json.RawMessage, name string, raw json.RawMessage) []byte {
	if !selfReportOnly() {
		return mcpToolErr(id, name+" はセッション側の Agent Fleet サーバー専用です")
	}
	if !mcpAgentMemoryEnabled {
		return mcpToolErr(id, "Agent Fleet memory is not enabled for this session (Settings > Agent memory)")
	}
	var a struct {
		Query       string   `json:"query"`
		Limit       int      `json:"limit"`
		Name        string   `json:"name"`
		Scope       string   `json:"scope"`
		Description string   `json:"description"`
		Body        string   `json:"body"`
		Type        string   `json:"type"`
		Kinds       []string `json:"kinds"`
		Revision    int      `json:"revision"`
		Budget      int      `json:"budget"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return mcpToolErr(id, name+" の引数を読めません: "+err.Error())
		}
	}
	// An owner that cannot be found still gets the user-wide scope; the Agent records the
	// author as unknown rather than refusing the call.
	owner, _ := mcpOwningSession()
	q := url.Values{}
	if owner != "" {
		q.Set("session", owner)
	}

	switch name {
	case mcpToolMemoryIndex:
		if a.Budget != 0 {
			q.Set("budget", fmt.Sprint(a.Budget)) // the Agent clamps it
		}
		out, err := agentDo(http.MethodGet, "/agents/memory/entries?"+q.Encode(), nil)
		if err != nil {
			return mcpToolErr(id, mcpMemoryErr(err))
		}
		return mcpTextResult(id, mcpMemoryFormatIndex(out))
	case mcpToolMemorySearch:
		q.Set("q", a.Query)
		if a.Limit > 0 {
			q.Set("limit", fmt.Sprint(a.Limit))
		}
		out, err := agentDo(http.MethodGet, "/agents/memory/entries/search?"+q.Encode(), nil)
		if err != nil {
			return mcpToolErr(id, mcpMemoryErr(err))
		}
		return mcpTextResult(id, mcpMemoryFormatSearch(out))
	case mcpToolMemoryRead:
		q.Set("name", a.Name)
		if a.Scope != "" {
			q.Set("scope", a.Scope)
		}
		out, err := agentDo(http.MethodGet, "/agents/memory/entries/read?"+q.Encode(), nil)
		if err != nil {
			return mcpToolErr(id, mcpMemoryErr(err))
		}
		return mcpTextResult(id, mcpMemoryFormatRead(out))
	case mcpToolMemorySave:
		body, _ := json.Marshal(map[string]any{
			"session": owner, "scope": a.Scope, "name": a.Name, "description": a.Description,
			"type": a.Type, "kinds": a.Kinds, "body": a.Body, "revision": a.Revision,
		})
		out, err := agentDo(http.MethodPost, "/agents/memory/entries", body)
		if err != nil {
			return mcpToolErr(id, mcpMemoryErr(err))
		}
		return mcpTextResult(id, mcpMemoryFormatSave(out))
	case mcpToolMemoryForget:
		body, _ := json.Marshal(map[string]any{"session": owner, "scope": a.Scope, "name": a.Name, "revision": a.Revision})
		out, err := agentDo(http.MethodPost, "/agents/memory/entries/forget", body)
		if err != nil {
			return mcpToolErr(id, mcpMemoryErr(err))
		}
		return mcpTextResult(id, out)
	}
	return mcpToolErr(id, "unknown memory tool: "+name)
}

// mcpMemoryFormatSave is the Agent's save answer, with its authoring warnings said again in
// plain text: the memory is saved either way, and the agent that wrote it is the one who can
// shorten it.
func mcpMemoryFormatSave(raw string) string {
	var r struct {
		Warnings []string `json:"warnings"`
	}
	if json.Unmarshal([]byte(raw), &r) != nil || len(r.Warnings) == 0 {
		return raw
	}
	return raw + "\n\nSaved, with guidance:\n- " + strings.Join(r.Warnings, "\n- ")
}

// mcpMemoryErr turns the Agent's refusal into what the agent should do next. The Agent's
// message already says it for conflicts and validation, so it is passed through; a secret
// refusal lists the rule and line (never the value) so the memory can be rewritten.
func mcpMemoryErr(err error) string {
	var he *agentHTTPError
	if !errors.As(err, &he) {
		return "Agent Fleet のメモリに接続できません（Workspace Agent）: " + err.Error()
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Findings []struct {
			Line int    `json:"line"`
			Rule string `json:"rule"`
			Hint string `json:"hint"`
		} `json:"findings"`
	}
	_ = json.Unmarshal([]byte(he.Body), &body)
	switch {
	case body.Error.Code == "memory_disabled":
		return "Agent Fleet memory is turned off in Settings > Agent memory; do not retry, and keep notes in your own memory instead."
	case body.Error.Code == "memory_secret_detected":
		parts := make([]string, 0, len(body.Findings))
		for _, f := range body.Findings {
			parts = append(parts, fmt.Sprintf("line %d: %s (%s)", f.Line, f.Rule, f.Hint))
		}
		return "Refused: the memory looks like it contains a secret (" + strings.Join(parts, "; ") +
			"). Rewrite it without the value — say where the secret is kept instead."
	case body.Error.Code == "" && he.StatusCode == http.StatusNotFound:
		return "この Workspace Agent には Agent Fleet のメモリがありません（ADR 0108 より前の版）"
	case body.Error.Message != "":
		return body.Error.Code + ": " + body.Error.Message
	}
	return he.Error()
}

// FormatMemoryIndex renders an index answer the way the memory_index tool prints it, for the one
// caller that has no MCP round trip: lcpp's per-session system prompt.
func FormatMemoryIndex(raw string) string { return mcpMemoryFormatIndex(raw) }

// mcpMemoryFormatIndex prints one line per described memory, then the names of the rest: an
// index is read every time work starts, so the Agent bounds it (memoryx.agentMemBudgetIndex)
// and the line text must stay identical to memoryx.agentMemIndexLine, which the budget measures.
func mcpMemoryFormatIndex(raw string) string {
	var v struct {
		Project   *mcpMemoryProject `json:"project"`
		Entries   []mcpMemoryEntry  `json:"entries"`
		More      []string          `json:"more"`
		Omitted   int               `json:"omitted"`
		Truncated bool              `json:"truncated"`
		Withheld  int               `json:"withheld"`

		// Pinned memories that did not fit the budget; see memoryx.agentMemBudgetIndex.
		PinnedOmitted int `json:"pinnedOmitted"`
	}
	if json.Unmarshal([]byte(raw), &v) != nil {
		return raw
	}
	var b strings.Builder
	if v.Project != nil {
		fmt.Fprintf(&b, "Project: %s\n", v.Project.Display)
	} else {
		b.WriteString("Project: none (this session has no working copy under ~/repos; user scope only)\n")
	}
	if v.Withheld > 0 {
		// Count only: a withheld file's name may be the very thing that failed the scan.
		fmt.Fprintf(&b, "%d memory file(s) are withheld because they look like they contain a secret or cannot be checked; tell your user, who has to fix them.\n", v.Withheld)
	}
	if len(v.Entries) == 0 && len(v.More) == 0 && v.Omitted == 0 {
		if v.Withheld == 0 {
			b.WriteString("No memories yet. Save what a later session should know with memory_save.\n")
		}
		return b.String()
	}
	for _, e := range v.Entries {
		fmt.Fprintf(&b, "- [%s] %s — %s%s\n", e.Scope, e.Name, e.Description, mcpMemoryTags(e))
	}
	// The Agent already ranked and cut; render exactly that and add nothing back.
	if len(v.More) > 0 {
		b.WriteString("Not listed above (names only; a name ending in \"…\" is a prefix — memory_search matches names, or memory_read with the full name):\n")
		b.WriteString(strings.Join(v.More, " "))
		b.WriteByte('\n')
	}
	if v.Omitted > 0 {
		fmt.Fprintf(&b, "and %d more (use memory_search)\n", v.Omitted)
	}
	if v.PinnedOmitted > 0 {
		fmt.Fprintf(&b, "%d of these are pinned by your user (over the index budget)\n", v.PinnedOmitted)
	}
	b.WriteString("Read one with memory_read before relying on it.\n")
	return b.String()
}

func mcpMemoryTags(e mcpMemoryEntry) string {
	var tags []string
	if e.Type != "" {
		tags = append(tags, e.Type)
	}
	if len(e.Kinds) > 0 {
		tags = append(tags, "for "+strings.Join(e.Kinds, ","))
	}
	if len(e.Updated) >= 10 {
		tags = append(tags, e.Updated[:10])
	}
	if len(tags) == 0 {
		return ""
	}
	return " (" + strings.Join(tags, "; ") + ")"
}

func mcpMemoryFormatSearch(raw string) string {
	var v struct {
		Hits []mcpMemoryEntry `json:"hits"`
	}
	if json.Unmarshal([]byte(raw), &v) != nil {
		return raw
	}
	if len(v.Hits) == 0 {
		return "No memory matches."
	}
	var b strings.Builder
	for _, e := range v.Hits {
		fmt.Fprintf(&b, "- [%s] %s — %s%s\n", e.Scope, e.Name, e.Description, mcpMemoryTags(e))
		for _, s := range e.Snippets {
			fmt.Fprintf(&b, "    %s\n", s)
		}
	}
	return b.String()
}

func mcpMemoryFormatRead(raw string) string {
	var e mcpMemoryEntry
	if json.Unmarshal([]byte(raw), &e) != nil {
		return raw
	}
	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\nscope: %s\nrevision: %d\ndescription: %s\n", e.Name, e.Scope, e.Revision, e.Description)
	if e.Type != "" {
		fmt.Fprintf(&b, "type: %s\n", e.Type)
	}
	if len(e.Kinds) > 0 {
		fmt.Fprintf(&b, "kinds: %s\n", strings.Join(e.Kinds, ", "))
	}
	fmt.Fprintf(&b, "author: %s (session %s), updated %s\n\n%s\n", e.AuthorKind, e.AuthorSession, e.Updated, e.Body)
	return b.String()
}
