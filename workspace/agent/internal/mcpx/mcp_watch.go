package mcpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// The session face of the watch engine (ADR 0133): watch subscribes the calling session to a
// pull request and the engine injects a notice when the condition occurs, so an agent stops
// running `gh pr checks --watch` in the background. Each tool acts on the caller's own watches
// only.
const (
	mcpToolWatch       = "watch"
	mcpToolUnwatch     = "unwatch"
	mcpToolListWatches = "list_watches"
)

// watchCallTimeout covers the engine's Resolve, which asks the provider once.
const watchCallTimeout = 40 * time.Second

type watchArgs struct {
	Source         string   `json:"source,omitempty"`
	Notify         string   `json:"notify,omitempty"`
	Ref            string   `json:"ref"`
	Types          []string `json:"types"`
	Until          string   `json:"until,omitempty"`
	ExpiresInHours int      `json:"expires_in_hours,omitempty"`
}

func mcpWatch(id json.RawMessage, a *watchArgs) []byte {
	owner, msg := watchCaller()
	if msg != "" {
		return mcpToolErr(id, msg)
	}
	body, _ := json.Marshal(a)
	out, err := agentDoTimeout(http.MethodPost, "/sessions/"+url.PathEscape(owner)+"/watches", body, watchCallTimeout)
	if err != nil {
		return watchErr(id, "watch", err)
	}
	return mcpTextResult(id, out)
}

func mcpUnwatch(id json.RawMessage, a *idArg) []byte {
	owner, msg := watchCaller()
	if msg != "" {
		return mcpToolErr(id, msg)
	}
	out, err := agentDo(http.MethodDelete, "/sessions/"+url.PathEscape(owner)+"/watches/"+url.PathEscape(a.ID), nil)
	if err != nil {
		return watchErr(id, "unwatch", err)
	}
	return mcpTextResult(id, out)
}

func mcpListWatches(id json.RawMessage) []byte {
	owner, msg := watchCaller()
	if msg != "" {
		return mcpToolErr(id, msg)
	}
	out, err := agentGET("/sessions/" + url.PathEscape(owner) + "/watches")
	if err != nil {
		return watchErr(id, "list_watches", err)
	}
	return mcpTextResult(id, out)
}

func watchCaller() (string, string) {
	if !selfReportOnly() {
		return "", "the watch tools are only offered to a session"
	}
	owner, err := mcpOwningSession()
	if err != nil {
		return "", "cannot tell which session is calling: " + err.Error()
	}
	return owner, ""
}

func watchErr(id json.RawMessage, tool string, err error) []byte {
	var he *agentHTTPError
	if errors.As(err, &he) && he.code() == "" && he.StatusCode == http.StatusNotFound {
		return mcpToolErr(id, "this Workspace Agent predates the watch engine; nothing was done")
	}
	return mcpToolErr(id, tool+" failed: "+agentErrDetail(err))
}

func watchSchema() map[string]any {
	return map[string]any{
		"name": mcpToolWatch,
		"description": "Agent Fleet: get a notice in this session when a pull request changes, instead of polling it yourself (no `gh pr checks --watch` in the background). " +
			"The notice arrives as your next input once you are idle, even after a restart; a stopped session is started for it. " +
			"Types: pr.checks.completed, pr.checks.failed, pr.conflict, pr.mergeable, pr.ready, pr.merged, pr.closed, pr.review.changes_requested, pr.review.threads_unresolved, or a prefix like pr.checks.*. " +
			"notify parent sends the notices to the session that started you instead; that watch then belongs to it. " +
			"A condition that already holds is reported at once. until first (default) ends the watch after one notice; deadline keeps it until it expires (default 7 days, at most 30).",
		"inputSchema": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"ref":              map[string]any{"type": "string", "description": "owner/repo#45, #45 in your own repository, or the PR's https://github.com/… URL"},
				"types":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "event types to be told about"},
				"until":            map[string]any{"type": "string", "enum": []string{"first", "deadline"}},
				"expires_in_hours": map[string]any{"type": "integer", "minimum": 1, "maximum": 720},
				"source":           map[string]any{"type": "string", "enum": []string{"github"}, "description": "default github"},
				"notify":           map[string]any{"type": "string", "enum": []string{"self", "parent"}, "description": "default self"},
			},
			"required": []string{"ref", "types"},
		},
	}
}

func unwatchSchema() map[string]any {
	return map[string]any{
		"name":        mcpToolUnwatch,
		"description": "Agent Fleet: end one of your watches by its id (from watch or list_watches).",
		"inputSchema": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{"id": map[string]any{"type": "string"}},
			"required":   []string{"id"},
		},
	}
}

func listWatchesSchema() map[string]any {
	return map[string]any{
		"name":        mcpToolListWatches,
		"description": "Agent Fleet: list your watches with their ids, types, expiry, and the last read of each subject (an error there means the provider is not answering).",
		"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}},
	}
}

// watchTools is part of every session's surface: each tool reads or writes only the caller's
// own watches.
func watchTools() []toolSpec {
	return []toolSpec{{
		name: mcpToolWatch, session: watchSchema,
		handle: handler(func(req mcpReq, p toolCallParams, a *watchArgs) []byte { return mcpWatch(req.ID, a) }),
	}, {
		name: mcpToolUnwatch, session: unwatchSchema,
		handle: handler(func(req mcpReq, p toolCallParams, a *idArg) []byte { return mcpUnwatch(req.ID, a) }),
	}, {
		name: mcpToolListWatches, session: listWatchesSchema,
		handle: handler(func(req mcpReq, p toolCallParams, a *noArgs) []byte { return mcpListWatches(req.ID) }),
	}}
}

// mcpStdioWatchTools is the watch family's session list.
func mcpStdioWatchTools() []map[string]any {
	return sessionTools(watchTools())
}
