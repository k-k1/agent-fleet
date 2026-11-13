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
			"GitHub Actions: ref is a run URL (https://github.com/o/r/actions/runs/<id>, optionally /attempts/<n>) or o/r:run/<id> for that run, which fires when it completes (at once if it already has; with until deadline a re-run is a new attempt and is told again), or o/r:<workflow file>.yml with an optional @branch (o/r:cli-pin-bump.yml@main) for the next run of that workflow (every branch and event unless @branch narrows it; the workflow URL takes no branch), which fires for a run that started after the watch is made or up to 10 minutes before it (a run chained from the one you just waited for starts seconds before your notice arrives; one that had already completed fires at once), never for an older one, and not for a re-run of an older one; when what you wait for is a PR the run opens, watch that PR (or the branch's PR) instead of the workflow's next run, and when a run is chained from one you just watched, check `gh run list` for it first; the workflow name and failed jobs arrive as quoted data. Such a ref is routed to source github_actions on its own. " +
			"Bitbucket Pipelines (source bitbucket, or a bitbucket.org URL): ref is a run URL (https://bitbucket.org/w/r/pipelines/results/<n>) or w/r:run/<n> for that run, or w/r:pipelines[/<custom pipeline name>][@branch] (w/r:pipelines/nightly@main) for the next run, which fires for a run that started after the watch is made or up to 10 minutes before it, never an older one; a cancelled or expired run counts as failed, and failed steps arrive as quoted data. Such a ref is routed to source bitbucket_pipelines. " +
			"GitHub releases and tags: github.release.published (a new non-draft release) and github.tag.created (a new tag), with ref o/r:releases[@pattern] or o/r:tags[@pattern] (a https://github.com/o/r/releases URL works as o/r:releases); o/r:releases+pre also counts pre-releases, which are skipped otherwise, and drafts never fire. The optional pattern narrows the tag name: letters, digits and . _ - / + with * (any run) and ? (one character), 64 characters at most, anything else is refused (o/r:releases@v2.*). Only a release published, or a tag created, after the watch is made fires; one that already exists never does. The notice carries the repo and tag; a release's name and notes arrive as quoted data. A repository with more than 300 tags is refused for a tags watch (its list cannot be read in full); use o/r:releases there. A tag created again after being deleted is told again. Such a ref is routed to source github_releases or github_tags on its own. " +
			"source time is a timer: ref is an RFC 3339 time with an offset (2026-11-09T15:00:00+09:00) or a wait (in 20m, 2h30m, 3d), at most 30 days ahead, and no types are needed; the notice type is time.fired. " +
			"source fleet watches another session of this Workspace: ref is its name (from list_child_sessions, or list_peer_sessions when peer messaging is on; never yourself) and the types are fleet.session.idle (it ended a turn and is idle; each turn end is one notice, and a turn that ended before the watch was made does not count, so watch right after you message it), fleet.session.stopped (it is stopped; at once if it already is) and fleet.quota.reopened (its usage limit lifted), or fleet.*. " +
			"To wait for a child or a reviewer, watch fleet.session.idle on it; do not set a time watch as a backstop. It fires whether or not the child also reports back, so unwatch once the report has arrived. For the four hook-less kinds (agy, copilot, cursor, kiro) the turn end is read off a poll and is best effort: a notice can come a poll late, and an idle left over from the previous turn that lasts longer than about 10 s after your message can be taken for the new turn's end (early or missed notice), so check the session's state (get_session_status) after the notice before acting on it. A session that is deleted ends the watch with a notice. " +
			"A timer fires while the Workspace runs; wake true also books the Control Plane to start the Workspace if it is stopped then (refused where the Control Plane has no scheduler). Without wake a timer that fell due while stopped is told late at the next start. " +
			"A condition that already holds is reported at once. until first (default) ends the watch after one notice; deadline keeps it until it expires (default 7 days, at most 30).",
		"inputSchema": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"ref":              map[string]any{"type": "string", "description": "owner/repo#45, #45 in your own repository, or the PR's URL; a session name for source fleet; a workflow run URL, o/r:run/<id>, o/r:<file>.yml[@branch] for GitHub Actions; o/r:releases[+pre][@pattern] or o/r:tags[@pattern] for GitHub releases and tags; a pipeline run URL, w/r:run/<n>, w/r:pipelines[/<name>][@branch] with source bitbucket for Bitbucket Pipelines; for source ext.<name>, whatever that server takes"},
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
