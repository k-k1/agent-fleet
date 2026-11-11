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
	Wake           bool     `json:"wake,omitempty"`
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
		"description": "Agent Fleet: get a notice in this session when a GitHub or Bitbucket pull request changes, instead of polling it yourself (no `gh pr checks --watch` in the background). " +
			"source ext.<name> watches through an MCP server of yours that is marked as a watch source: ref is one line of printable ASCII (200 characters at most) that the server understands, and the types are that server's own, ext.<name>.* (the server's text arrives as quoted data). " +
			"The notice arrives as your next input once you are idle, even after a restart; a stopped session is started for it. " +
			"Types: pr.checks.completed, pr.checks.failed, pr.conflict, pr.mergeable, pr.ready, pr.merged, pr.closed, pr.review.changes_requested, pr.review.threads_unresolved (GitHub only), or a prefix like pr.checks.*. source bitbucket for a Bitbucket PR. " +
			"notify parent sends the notices to the session that started you instead; that watch then belongs to it. " +
			"CI runs not tied to a PR (GitHub Actions, Bitbucket Pipelines) use the same types on both providers: ci.run.completed (every finished run) and ci.run.failed (only failures), or ci.run.*; the old github.workflow_run.* names still work as aliases. " +
			"GitHub Actions: ref is a run URL (https://github.com/o/r/actions/runs/<id>, optionally /attempts/<n>) or o/r:run/<id> for that run, which fires when it completes (at once if it already has; with until deadline a re-run is a new attempt and is told again), or o/r:<workflow file>.yml with an optional @branch (o/r:cli-pin-bump.yml@main) for the next run of that workflow (every branch and event unless @branch narrows it; the workflow URL takes no branch), which fires only for a run that starts after the watch is made, and not for a re-run of an older one; the workflow name and failed jobs arrive as quoted data. Such a ref is routed to source github_actions on its own. " +
			"Bitbucket Pipelines (source bitbucket, or a bitbucket.org URL): ref is a run URL (https://bitbucket.org/w/r/pipelines/results/<n>) or w/r:run/<n> for that run, or w/r:pipelines[/<custom pipeline name>][@branch] (w/r:pipelines/nightly@main) for the next run, which fires only for a run that starts after the watch is made; a cancelled or expired run counts as failed, and failed steps arrive as quoted data. Such a ref is routed to source bitbucket_pipelines. " +
			"source time is a timer: ref is an RFC 3339 time with an offset (2026-11-09T15:00:00+09:00) or a wait (in 20m, 2h30m, 3d), at most 30 days ahead, and no types are needed; the notice type is time.fired. " +
			"A timer fires while the Workspace runs; wake true also books the Control Plane to start the Workspace if it is stopped then (refused where the Control Plane has no scheduler). Without wake a timer that fell due while stopped is told late at the next start. " +
			"A condition that already holds is reported at once. until first (default) ends the watch after one notice; deadline keeps it until it expires (default 7 days, at most 30).",
		"inputSchema": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"ref":              map[string]any{"type": "string", "description": "owner/repo#45, #45 in your own repository, or the PR's URL; a workflow run URL, o/r:run/<id>, o/r:<file>.yml[@branch] for GitHub Actions; a pipeline run URL, w/r:run/<n>, w/r:pipelines[/<name>][@branch] with source bitbucket for Bitbucket Pipelines; for source ext.<name>, whatever that server takes"},
				"types":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "event types to be told about (not needed for source time)"},
				"wake":             map[string]any{"type": "boolean", "description": "source time only: start the stopped Workspace when the timer is due"},
				"until":            map[string]any{"type": "string", "enum": []string{"first", "deadline"}},
				"expires_in_hours": map[string]any{"type": "integer", "minimum": 1, "maximum": 720},
				"source":           map[string]any{"type": "string", "description": "github (default), bitbucket, time, or ext.<name> for an MCP server of yours that is marked as a watch source (its types start with ext.<name>.)"},
				"notify":           map[string]any{"type": "string", "enum": []string{"self", "parent"}, "description": "default self"},
			},
			"required": []string{"ref"},
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
